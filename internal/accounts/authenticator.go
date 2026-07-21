package accounts

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/starvy/flagfish/internal/auth"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// JobInserter enqueues a job on the caller's transaction, so the enqueue commits or
// rolls back with the work that justified it. *river.Client[pgx.Tx] satisfies it.
type JobInserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// noJobs is the default when no queue is wired. It refuses rather than drops: a Service
// asked to enqueue mail it cannot enqueue must fail loudly, not swallow the request.
type noJobs struct{}

func (noJobs) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, errors.New("accounts: no job queue is wired; cannot enqueue mail")
}

// Service owns credentials and is the auth.Authenticator.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	mode account.Mode
	log  *slog.Logger
	jobs JobInserter
}

// Option configures a Service at construction. The job queue is optional so the OpenAPI
// renderer and the older test harnesses can build a Service with no River behind it.
type Option func(*Service)

// WithJobs supplies the inserter the email flows enqueue through.
func WithJobs(jobs JobInserter) Option { return func(s *Service) { s.jobs = jobs } }

func NewService(pool *pgxpool.Pool, mode account.Mode, log *slog.Logger, opts ...Option) *Service {
	s := &Service{pool: pool, q: db.New(pool), mode: mode, log: log, jobs: noJobs{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Compile-time proof that the seam is filled. Without this, a refactor that changed the
// interface would fail somewhere in main instead of here.
var _ auth.Authenticator = (*Service)(nil)

// ErrUnauthorized means a credential was presented and was bad. It is the answer for a
// dead API token: a request bearing one is a 401 and must not silently downgrade to
// anonymous, because that is how an expired token quietly becomes "public access" and
// nobody notices until the audit.
//
// A stale session cookie is deliberately NOT treated this way — see Authenticate. A token
// is presented by a program that can react to a 401; a cookie is re-sent by a browser on
// its own, is HttpOnly so the SPA cannot drop it, and outlives every logout, expiry and
// password change. 401ing it would wall the browser out of POST /login itself.
var ErrUnauthorized = errors.New("accounts: invalid or expired credential")

// Authenticate resolves a request to a Principal, from either credential.
//
// This function existing exactly once is the entire fix for the ban bypass. Resolving a
// session in one request hook and a token in another, with the ban wall running between
// them, lets a banned user holding an API token keep full API access including flag
// submission: the wall sees an anonymous request and returns before the token hook logs
// the user in.
//
// Here both credentials converge on one Principal before any authorization middleware
// runs. The ban wall downstream reads Principal.Banned and cannot tell how the caller
// authenticated — so a token cannot route around a wall that a cookie hits. The bug is
// not "fixed" so much as made unrepresentable.
//
// Token is checked first, and both paths end in the same loadPrincipal call.
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (auth.Auth, error) {
	if tok, ok := bearerToken(r.Header.Get("Authorization")); ok {
		// A bad token is a failed auth attempt and stays a 401 — see ErrUnauthorized.
		return s.authenticateToken(ctx, tok)
	}
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		a, err := s.authenticateSession(ctx, c.Value)
		if errors.Is(err, ErrUnauthorized) {
			// A present-but-invalid session cookie is a logged-out browser, not an attacker:
			// expired, deleted, or fingerprint-killed by a password change. It degrades to
			// anonymous rather than 401 so the browser can still reach POST /login — the policy
			// layer denies anonymous callers everything a fresh anonymous caller cannot reach,
			// so nothing protected is exposed by the downgrade. A DB failure underneath is not
			// ErrUnauthorized and still propagates as an error, becoming a 503, not a silent pass.
			return auth.Auth{Method: auth.MethodAnonymous}, nil
		}
		return a, err
	}
	return auth.Auth{Method: auth.MethodAnonymous}, nil
}

func (s *Service) authenticateToken(ctx context.Context, token string) (auth.Auth, error) {
	row, err := s.q.GetAPIToken(ctx, digestOf(token))
	if errors.Is(err, pgx.ErrNoRows) {
		// Unknown or expired — the query cannot tell us which, deliberately, and neither
		// can the caller.
		return auth.Auth{}, ErrUnauthorized
	} else if err != nil {
		return auth.Auth{}, fmt.Errorf("accounts: token lookup: %w", err)
	}

	pr, err := s.loadPrincipal(ctx, row.UserID)
	if err != nil {
		return auth.Auth{}, err
	}

	// CSRFToken stays empty: token auth is CSRF-exempt, and it is exempt because the
	// identity came from a token — not because some header happened to be present on the
	// request. That distinction is what stops a CSRF bypass by header-stuffing.
	return auth.Auth{Principal: pr, Method: auth.MethodToken}, nil
}

func (s *Service) authenticateSession(ctx context.Context, sid string) (auth.Auth, error) {
	sess, err := s.q.GetSession(ctx, digestOf(sid))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Auth{}, ErrUnauthorized // missing or expired; the query enforces expiry
	} else if err != nil {
		return auth.Auth{}, fmt.Errorf("accounts: session lookup: %w", err)
	}

	// The password-change kill switch. The session recorded a fingerprint of the password
	// hash it was minted against; if the current hash no longer matches, the password has
	// changed since, and every session issued before that change is dead. No revocation
	// list, no fan-out delete, and no window.
	//
	// Constant-time because this compares a secret-derived value, and because there is no
	// reason not to.
	current := pwFingerprint(sess.PasswordHash)
	if subtle.ConstantTimeCompare(current, sess.PwFingerprint) != 1 {
		return auth.Auth{}, ErrUnauthorized
	}

	pr, err := s.loadPrincipal(ctx, sess.UserID)
	if err != nil {
		return auth.Auth{}, err
	}

	return auth.Auth{
		Principal: pr,
		Method:    auth.MethodCookie,
		CSRFToken: sess.CsrfToken,
	}, nil
}

// loadPrincipal is the one place a Principal is built. Both credentials land here.
func (s *Service) loadPrincipal(ctx context.Context, userID int64) (policy.Principal, error) {
	row, err := s.q.LoadPrincipal(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		// The credential outlived its user (deleted mid-session). Not anonymous — the
		// caller presented something, and it is no longer valid.
		return policy.Principal{}, ErrUnauthorized
	} else if err != nil {
		return policy.Principal{}, fmt.Errorf("accounts: load principal: %w", err)
	}

	// AccountID collapses user/team per the instance mode. A teamless user in teams mode
	// has no account at all; that is not an error here — it is Principal.Teamless, which
	// the policy layer gates on.
	var acct account.ID
	if id, err := s.mode.Resolve(account.Membership{UserID: row.UserID, TeamID: row.TeamID}); err == nil {
		acct = id
	}

	return policy.Principal{
		Authed:  true,
		IsAdmin: row.IsAdmin,

		Verified:   row.Verified,
		Banned:     row.Banned,
		TeamBanned: row.TeamBanned,
		Teamless:   row.Teamless,

		ProfileComplete:     row.ProfileComplete,
		TeamProfileComplete: row.TeamProfileComplete,
		ForcePasswordChange: row.MustChangePassword,

		UserID:    row.UserID,
		AccountID: acct,
	}, nil
}
