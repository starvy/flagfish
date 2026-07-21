package accounts

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/starvy/flagfish/internal/db"
)

// SessionTTL is how long a cookie lives.
const SessionTTL = 7 * 24 * time.Hour

// TokenTTL is the default API-token lifetime. "Never expires" is deliberately not
// expressible: an immortal credential is one nobody remembers issuing.
const TokenTTL = 30 * 24 * time.Hour

// A Session is what Login hands back.
type Session struct {
	ID        string // the plaintext id. Goes in the cookie; never stored.
	CSRFToken string
	ExpiresAt time.Time
	UserID    int64
}

// dummyHash is verified against when the email is unknown.
//
// Without it, a login for a nonexistent address returns as fast as the database can say
// "no row", while a real address pays for a full Argon2id verification — a timing oracle
// that enumerates every registered player. So an unknown user pays the same cost as a
// known one, and the answer is the same either way.
var dummyHash = mustHash("flagfish-user-enumeration-is-not-a-feature")

func mustHash(s string) string {
	h, err := Hash(s)
	if err != nil {
		panic("accounts: cannot hash the dummy password: " + err.Error())
	}
	return h
}

// Login verifies a password and mints a session.
//
// The rehash-on-login upgrade lives here: an imported bcrypt hash verifies, and because
// the plaintext is in hand exactly once — right now — it is silently re-hashed to
// Argon2id and written back. The bcrypt population drains to zero on its own, with no
// migration script and no mass password-reset email.
func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	u, err := s.q.GetUserByEmail(ctx, email)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Burn the same CPU a real verification would, then fail identically. Do not
		// short-circuit this: the whole point is that the two paths are indistinguishable.
		Verify(dummyHash, password)
		return Session{}, ErrBadCredentials
	case err != nil:
		return Session{}, fmt.Errorf("accounts: login: %w", err)
	}

	if u.PasswordHash == nil {
		Verify(dummyHash, password) // an OAuth-only account has no password to verify
		return Session{}, ErrBadCredentials
	}

	ok, rehash := Verify(*u.PasswordHash, password)
	if !ok {
		return Session{}, ErrBadCredentials
	}

	// A banned user is refused a new session, but this is not the ban wall and must not
	// be mistaken for it: the wall is in the middleware and covers every request on every
	// credential. This is only here so a ban takes effect at the front door too.
	if u.Banned {
		return Session{}, ErrBadCredentials
	}

	hash := *u.PasswordHash
	if rehash {
		upgraded, herr := Hash(password)
		if herr != nil {
			// Do not fail the login over it: the user's password is correct and the old
			// hash is still valid. Log it and move on — a failed upgrade is an operational
			// problem, not this player's problem.
			s.log.ErrorContext(ctx, "password rehash failed; the old hash is still in use",
				"error", herr, "user_id", u.ID)
		} else if uerr := s.q.UpdatePasswordHash(ctx, db.UpdatePasswordHashParams{
			UserID: u.ID, PasswordHash: &upgraded,
		}); uerr != nil {
			s.log.ErrorContext(ctx, "password rehash could not be persisted",
				"error", uerr, "user_id", u.ID)
		} else {
			// The session below must be fingerprinted against the hash we just wrote, not
			// the one we read. Otherwise the very session we are about to mint fails its
			// own fingerprint check on the next request and the user is logged straight
			// back out — a bug that would look exactly like "login is broken for imported
			// users", which is the population this code path exists for.
			hash = upgraded
		}
	}

	return s.mintSession(ctx, u.ID, hash)
}

func (s *Service) mintSession(ctx context.Context, userID int64, passwordHash string) (Session, error) {
	return mintSessionQ(ctx, s.q, userID, passwordHash)
}

// mintSessionQ takes the querier so a caller that is already mid-transaction can mint the
// replacement session on it, rather than committing a password write and then racing to
// create a session against the pool.
func mintSessionQ(ctx context.Context, q *db.Queries, userID int64, passwordHash string) (Session, error) {
	sid, idHash, err := NewSessionID()
	if err != nil {
		return Session{}, err
	}
	csrf, _, err := NewSessionID() // a second independent secret; not derived from the sid
	if err != nil {
		return Session{}, err
	}

	expires := time.Now().Add(SessionTTL)
	if _, err := q.CreateSession(ctx, db.CreateSessionParams{
		IDHash:        idHash,
		UserID:        userID,
		PwFingerprint: pwFingerprint(&passwordHash),
		CsrfToken:     csrf,
		ExpiresAt:     pgtype.Timestamptz{Time: expires, Valid: true},
	}); err != nil {
		return Session{}, fmt.Errorf("accounts: create session: %w", err)
	}

	return Session{ID: sid, CSRFToken: csrf, ExpiresAt: expires, UserID: userID}, nil
}

// Logout kills one session.
func (s *Service) Logout(ctx context.Context, sid string) error {
	if err := s.q.DeleteSession(ctx, digestOf(sid)); err != nil {
		return fmt.Errorf("accounts: logout: %w", err)
	}
	return nil
}

// ChangePassword sets a new password, revokes the account's API tokens, and mints a
// replacement session. It reports how many tokens it revoked.
//
// Every other session dies as a consequence of the write, without this function deleting
// anything: each session carries a fingerprint of the password hash it was minted against,
// and they all stop matching the moment the hash changes.
//
// API tokens are deleted explicitly, because nothing about them tracks the password. They go
// for the same reason the other sessions do: a logged-in user reaching for "change my
// password" is the first and most common response to a suspected compromise, and if the
// tokens survive it, the attacker keeps API access — flag submission included — while the
// user believes they have locked the door. The platform cannot tell routine rotation from
// panic, so it must assume panic; the cheap failure is re-minting a token, the expensive one
// is a live intruder. The count comes back so this is never silent.
//
// Password write, revocation and the new session all ride one transaction: a committed
// password with surviving tokens is precisely the state this is here to prevent.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next string) (Session, int64, error) {
	row, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return Session{}, 0, fmt.Errorf("accounts: change password: %w", err)
	}
	if row.PasswordHash == nil {
		return Session{}, 0, ErrBadCredentials
	}
	if ok, _ := Verify(*row.PasswordHash, current); !ok {
		return Session{}, 0, ErrBadCredentials
	}

	hash, err := Hash(next)
	if err != nil {
		return Session{}, 0, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, 0, fmt.Errorf("accounts: change password: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	// A real change also discharges a pending forced change — unlike the login rehash, which
	// re-mints the same password and must leave the flag alone.
	if err := q.UpdatePasswordAndClearForcedChange(ctx, db.UpdatePasswordAndClearForcedChangeParams{
		UserID: userID, PasswordHash: &hash,
	}); err != nil {
		return Session{}, 0, fmt.Errorf("accounts: change password: %w", err)
	}

	revoked, rerr := q.DeleteUserAPITokens(ctx, userID)
	if rerr != nil {
		return Session{}, 0, fmt.Errorf("accounts: change password: revoke api tokens for user %d: %w", userID, rerr)
	}

	sess, serr := mintSessionQ(ctx, q, userID, hash)
	if serr != nil {
		return Session{}, 0, serr
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, 0, fmt.Errorf("accounts: change password: %w", err)
	}
	return sess, revoked, nil
}

// SessionCookieFor builds the Set-Cookie for a session.
//
// HttpOnly: JavaScript must not be able to read it, which is what stops an XSS from
// becoming a session theft. SameSite=Lax: the cookie is not attached to cross-site POSTs
// at all, which is defence in depth behind the CSRF token rather than a replacement for
// it. Secure is the caller's decision: TLS usually dies at a proxy, so the transport layer is the
// only place that can tell whether the browser will be speaking HTTPS to us.
func SessionCookieFor(sess Session, secure bool) *http.Cookie {
	//nolint:gosec // G124 fires on any cookie literal; HttpOnly/SameSite are set right below.
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    sess.ID,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// ClearSessionCookie expires the cookie.
func ClearSessionCookie(secure bool) *http.Cookie {
	//nolint:gosec // G124 fires on any cookie literal; HttpOnly/SameSite are set right below.
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// CheckCSRF compares the presented token against the session's, in constant time.
func CheckCSRF(presented, expected string) bool {
	if presented == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}
