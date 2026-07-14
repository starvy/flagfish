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
	sid, idHash, err := NewSessionID()
	if err != nil {
		return Session{}, err
	}
	csrf, _, err := NewSessionID() // a second independent secret; not derived from the sid
	if err != nil {
		return Session{}, err
	}

	expires := time.Now().Add(SessionTTL)
	if _, err := s.q.CreateSession(ctx, db.CreateSessionParams{
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

// ChangePassword sets a new password.
//
// Every other session dies as a consequence, without this function deleting anything:
// each session carries a fingerprint of the password hash it was minted against, and
// they all stop matching the moment the hash changes. The caller re-mints its own.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next string) (Session, error) {
	row, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return Session{}, fmt.Errorf("accounts: change password: %w", err)
	}
	if row.PasswordHash == nil {
		return Session{}, ErrBadCredentials
	}
	if ok, _ := Verify(*row.PasswordHash, current); !ok {
		return Session{}, ErrBadCredentials
	}

	hash, err := Hash(next)
	if err != nil {
		return Session{}, err
	}
	if err := s.q.UpdatePasswordHash(ctx, db.UpdatePasswordHashParams{
		UserID: userID, PasswordHash: &hash,
	}); err != nil {
		return Session{}, fmt.Errorf("accounts: change password: %w", err)
	}

	return s.mintSession(ctx, userID, hash)
}

// SessionCookieFor builds the Set-Cookie for a session.
//
// HttpOnly: JavaScript must not be able to read it, which is what stops an XSS from
// becoming a session theft. SameSite=Lax: the cookie is not attached to cross-site POSTs
// at all, which is defence in depth behind the CSRF token rather than a replacement for
// it. Secure is set unless we are plainly on localhost.
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
