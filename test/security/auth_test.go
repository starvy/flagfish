//go:build integration

package security

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/starvy/flagfish/internal/accounts"
)

const pw = "correct horse battery staple"

// The ban wall covers token auth.
//
// This is the headline. A ban check that runs on the session id while the API-token hook
// sets that key *after* the check has already run and returned lets a banned user holding
// a valid token keep full API access — including flag submission. Combining banned + token
// is exactly the case such a design fails to cover.
//
// Here both credentials resolve to a Principal at one point, before any authorization
// runs, so the wall cannot tell them apart. The test asserts the cookie and the token
// are refused identically — because "identically" is the property, not "both non-200".
func TestS1_BanWallCoversBothCredentials(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	uid := f.user("victim", pw)

	// Mint both credentials while the account is in good standing — the attacker already
	// holds them when the ban lands. That is the realistic order and the dangerous one.
	sess, err := f.acct.Login(ctx, "victim@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	tok, err := f.acct.CreateToken(ctx, uid, nil, time.Hour)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	// Both work before the ban, or the test proves nothing.
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode; got != http.StatusOK {
		t.Fatalf("cookie before ban: %d, want 200", got)
	}
	if got := f.do(http.MethodGet, "/api/v1/probe", withToken(tok.Plaintext)).StatusCode; got != http.StatusOK {
		t.Fatalf("token before ban: %d, want 200 — token auth is not working at all", got)
	}

	f.ban(uid)

	cookie := f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode
	token := f.do(http.MethodGet, "/api/v1/probe", withToken(tok.Plaintext)).StatusCode

	if cookie != http.StatusForbidden {
		t.Errorf("banned + cookie: %d, want 403", cookie)
	}
	// The invariant.
	if token != http.StatusForbidden {
		t.Errorf("banned + token: %d, want 403 — a banned user kept API access via a token, "+
			"the authz bypass this architecture exists to make unrepresentable", token)
	}
	if cookie != token {
		t.Errorf("the two credentials were treated differently (cookie %d, token %d). "+
			"The ban wall must not be able to tell them apart — that is the whole fix.", cookie, token)
	}
}

// An expired token is refused, and does not silently degrade to anonymous.
//
// The dangerous failure is not "expired token works". It is "expired token is ignored,
// the request proceeds as anonymous, and a public endpoint answers it 200" — which looks
// like success to the client and hides the expiry entirely.
func TestS2_ExpiredTokenIsRejectedNotDowngraded(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	uid := f.user("expiring", pw)
	tok, err := f.acct.CreateToken(ctx, uid, nil, time.Hour)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	if _, err := f.pool.Exec(ctx,
		`UPDATE api_tokens SET expires_at = now() - interval '1 second' WHERE user_id = $1`, uid); err != nil {
		t.Fatalf("expire: %v", err)
	}

	if got := f.do(http.MethodGet, "/api/v1/probe", withToken(tok.Plaintext)).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("expired token: %d, want 401 (a downgrade to anonymous would be a silent auth bypass)", got)
	}

	// And the credential is dead at the service layer too, not just at the edge.
	if _, err := f.acct.Authenticate(ctx, tokenRequest(t, f, tok.Plaintext)); !errors.Is(err, accounts.ErrUnauthorized) {
		t.Errorf("Authenticate on an expired token: err = %v, want ErrUnauthorized", err)
	}
}

// A garbage or forged token is refused.
func TestS3_ForgedTokenIsRejected(t *testing.T) {
	f := setup(t)
	f.user("real", pw)

	for _, tok := range []string{
		"flagfish_" + strings.Repeat("a", 64), // right shape, never issued
		"flagfish_",                           // empty secret
		"not-even-close",
	} {
		if got := f.do(http.MethodGet, "/api/v1/probe", withToken(tok)).StatusCode; got == http.StatusOK {
			t.Errorf("forged token %q was accepted", tok)
		}
	}
}

// Changing a password kills every other session.
//
// There is no revocation list and no fan-out delete: each session records a fingerprint
// of the password hash it was minted against, and they all stop matching the instant the
// hash changes. This is the property that makes "someone stole my cookie" recoverable by
// the user alone, without an admin.
func TestS4_PasswordChangeInvalidatesOtherSessions(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	uid := f.user("owner", pw)

	stolen, err := f.acct.Login(ctx, "owner@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(stolen.ID)).StatusCode; got != http.StatusOK {
		t.Fatalf("stolen cookie should work before the change: %d", got)
	}

	fresh, _, err := f.acct.ChangePassword(ctx, uid, pw, "a totally different password")
	if err != nil {
		t.Fatalf("change password: %v", err)
	}

	// The thief's cookie is dead: the fingerprint no longer matches, so it degrades to
	// anonymous and reaches nothing behind the auth wall. That is a policy denial (403), the
	// same one a logged-out browser gets — not the old blanket 401 on the cookie, which used to
	// wall the *owner* out of re-logging in from the same browser too.
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(stolen.ID)).StatusCode; got != http.StatusForbidden {
		t.Errorf("the OLD session reached a protected route after a password change: %d, want 403", got)
	}
	// …and the owner's new one is not.
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(fresh.ID)).StatusCode; got != http.StatusOK {
		t.Errorf("the session minted BY the password change does not work: %d, want 200", got)
	}
}

// CSRF is required on cookie writes, and exempt for token writes.
//
// The exemption is because the identity came from a token, not because a header was
// present. An attacker who staples an Authorization header onto a forged cross-site
// request does not become token-authenticated — they fail to authenticate at all.
func TestS5_CSRF(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	uid := f.user("writer", pw)
	sess, err := f.acct.Login(ctx, "writer@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	tok, err := f.acct.CreateToken(ctx, uid, nil, time.Hour)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	t.Run("cookie write without a CSRF token is refused", func(t *testing.T) {
		if got := f.do(http.MethodPost, "/api/v1/probe", withCookie(sess.ID)).StatusCode; got != http.StatusForbidden {
			t.Errorf("status = %d, want 403", got)
		}
	})

	t.Run("cookie write with a wrong CSRF token is refused", func(t *testing.T) {
		got := f.do(http.MethodPost, "/api/v1/probe", withCookie(sess.ID), withCSRF("not-the-token")).StatusCode
		if got != http.StatusForbidden {
			t.Errorf("status = %d, want 403", got)
		}
	})

	t.Run("cookie write with the right CSRF token succeeds", func(t *testing.T) {
		got := f.do(http.MethodPost, "/api/v1/probe", withCookie(sess.ID), withCSRF(sess.CSRFToken)).StatusCode
		if got != http.StatusOK {
			t.Errorf("status = %d, want 200", got)
		}
	})

	t.Run("cookie read needs no CSRF token", func(t *testing.T) {
		if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode; got != http.StatusOK {
			t.Errorf("status = %d, want 200 — CSRF must not gate safe methods", got)
		}
	})

	t.Run("token write is CSRF-exempt", func(t *testing.T) {
		if got := f.do(http.MethodPost, "/api/v1/probe", withToken(tok.Plaintext)).StatusCode; got != http.StatusOK {
			t.Errorf("status = %d, want 200 — a bearer token is not attached by a browser, "+
				"so there is nothing for a cross-site request to ride on", got)
		}
	})

	t.Run("a stapled Authorization header does not exempt a cookie write", func(t *testing.T) {
		// The forgery attempt: keep the victim's cookie, add a junk bearer header, hope the
		// CSRF check keys off "a token header is present" rather than off the identity.
		got := f.do(http.MethodPost, "/api/v1/probe",
			withCookie(sess.ID), withToken("flagfish_"+strings.Repeat("f", 64))).StatusCode
		if got == http.StatusOK {
			t.Error("a junk Authorization header bypassed CSRF — the exemption is keyed off the " +
				"presence of a header instead of off the authenticated identity")
		}
	})
}

// An unauthenticated caller is denied.
func TestS6_AnonymousIsDenied(t *testing.T) {
	f := setup(t)
	if got := f.do(http.MethodGet, "/api/v1/probe").StatusCode; got == http.StatusOK {
		t.Errorf("anonymous access to an auth-required route returned 200")
	}
}

// S6b — an admin, banned after minting a session, is still walled. Admins get no ban
// exemption; the wall reads the Principal, not the role.
func TestS6b_BannedAdminIsWalled(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	uid := f.user("rogue-admin", pw, asAdmin)
	sess, err := f.acct.Login(ctx, "rogue-admin@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode; got != http.StatusOK {
		t.Fatalf("admin before ban: %d, want 200", got)
	}

	f.ban(uid)
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode; got != http.StatusForbidden {
		t.Errorf("a banned admin reached the route: %d, want 403 — admins get no ban exemption", got)
	}
}

// Rate limiting is exact, and fails closed.
//
// A counter that is atomic only on Redis silently stops limiting on any other backend while
// still returning 200. Here the counter is a Postgres row and the increment is an atomic upsert.
func TestS7_RateLimitHoldsUnderConcurrency(t *testing.T) {
	const limit = 20
	wholeWindow(t, 20*time.Second)
	f := setup(t, withLimit(limit))
	ctx := context.Background()

	f.user("flooder", pw)
	sess, err := f.acct.Login(ctx, "flooder@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	const attempts = 100
	codes := make([]int, attempts)
	done := make(chan struct{})
	for i := range attempts {
		go func() {
			defer func() { done <- struct{}{} }()
			codes[i] = f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode
		}()
	}
	for range attempts {
		<-done
	}

	allowed, limited := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			allowed++
		case http.StatusTooManyRequests:
			limited++
		}
	}

	// Exactly the limit got through. Not limit±3 because increments interleaved.
	if allowed != limit {
		t.Errorf("allowed = %d, want exactly %d — increments were lost, which is what a "+
			"non-atomic counter does under precisely this load", allowed, limit)
	}
	if limited != attempts-limit {
		t.Errorf("limited = %d, want %d", limited, attempts-limit)
	}
}

// An imported bcrypt password verifies, and is silently upgraded to Argon2id.
//
// Shipping Argon2id-only would lock out every imported user, which is the sort of thing
// discovered on the morning of a migration. The upgrade happens at login because that is
// the one moment the plaintext is in hand.
func TestS8_BcryptVerifiesAndRehashesOnLogin(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// A bcrypt hash, exactly as an import would carry.
	bh, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	uid := f.user("migrated", pw, withHash(string(bh)))

	var stored string
	if serr := f.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, uid).Scan(&stored); serr != nil {
		t.Fatalf("read hash: %v", serr)
	}
	if !strings.HasPrefix(stored, "$2") {
		t.Fatalf("fixture did not store a bcrypt hash: %q", stored)
	}

	sess, err := f.acct.Login(ctx, "migrated@ctf.test", pw)
	if err != nil {
		t.Fatalf("an imported bcrypt user cannot log in: %v", err)
	}

	// Rehashed in place…
	if err := f.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, uid).Scan(&stored); err != nil {
		t.Fatalf("re-read hash: %v", err)
	}
	if !strings.HasPrefix(stored, "$argon2id$") {
		t.Errorf("password_hash = %q, want an argon2id hash — the bcrypt population never drains", stored)
	}

	// …and the session minted during that rehash must still work. It is fingerprinted
	// against the password hash, and the hash just changed underneath it — get this wrong
	// and every imported user is logged out immediately after logging in, which would look
	// exactly like "login is broken for migrated users".
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode; got != http.StatusOK {
		t.Errorf("the session minted during the rehash is already dead: %d, want 200", got)
	}

	// And the next login still works against the new hash.
	if _, err := f.acct.Login(ctx, "migrated@ctf.test", pw); err != nil {
		t.Errorf("login after rehash: %v", err)
	}
}

// Login does not leak whether an address is registered.
//
// A failed login must be indistinguishable between "no such user" and "wrong password",
// in the error AND in the time it takes. Without the dummy-hash path an unknown address
// returns as fast as the database can say "no row" while a known one pays for a full
// Argon2id verification — which enumerates every registered player.
func TestS9_LoginDoesNotLeakUserExistence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	f.user("known", pw)

	_, errKnown := f.acct.Login(ctx, "known@ctf.test", "wrong password")
	_, errUnknown := f.acct.Login(ctx, "nobody@ctf.test", "wrong password")

	if !errors.Is(errKnown, accounts.ErrBadCredentials) || !errors.Is(errUnknown, accounts.ErrBadCredentials) {
		t.Fatalf("both must be ErrBadCredentials: known=%v unknown=%v", errKnown, errUnknown)
	}
	if errKnown.Error() != errUnknown.Error() {
		t.Errorf("the two failures are distinguishable:\n known:   %v\n unknown: %v", errKnown, errUnknown)
	}

	// Timing. Not a benchmark — just an assertion that the unknown-user path is not an
	// order of magnitude faster, which is what a missing dummy-verify looks like.
	known := timeLogin(t, f, "known@ctf.test")
	unknown := timeLogin(t, f, "nobody@ctf.test")
	if unknown*4 < known {
		t.Errorf("the unknown-address path is %v vs %v for a known one — that ratio is a "+
			"user-enumeration oracle (the dummy verify is missing)", unknown, known)
	}
}

func timeLogin(t *testing.T, f *fixture, email string) time.Duration {
	t.Helper()
	const n = 3
	start := time.Now()
	for range n {
		// The error is the point — every one of these fails. We are timing the failure.
		if _, err := f.acct.Login(context.Background(), email, "wrong password"); err == nil {
			t.Fatal("a login with a wrong password succeeded")
		}
	}
	return time.Since(start) / n
}

// A token belonging to someone else cannot be deleted.
func TestS10_TokenDeleteIsOwnershipScoped(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	victim := f.user("victim2", pw)
	attacker := f.user("attacker", pw)

	tok, err := f.acct.CreateToken(ctx, victim, nil, time.Hour)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	if err := f.acct.DeleteToken(ctx, attacker, tok.ID); !errors.Is(err, accounts.ErrTokenNotFound) {
		t.Errorf("attacker deleting the victim's token: err = %v, want ErrTokenNotFound", err)
	}
	if n := f.count(`SELECT count(*) FROM api_tokens WHERE user_id = $1`, victim); n != 1 {
		t.Errorf("the victim's token was deleted by another user")
	}

	// The owner can.
	if err := f.acct.DeleteToken(ctx, victim, tok.ID); err != nil {
		t.Errorf("owner deleting their own token: %v", err)
	}
}

// tokenRequest builds a bare request carrying a bearer token, for the service-level
// assertions that do not go through the HTTP server.
func tokenRequest(t *testing.T, f *fixture, tok string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.server.URL+"/api/v1/probe", http.NoBody)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}
