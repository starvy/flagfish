//go:build integration

package security

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/accounts"
)

// A stale session cookie is a logged-out browser, not an attacker — it degrades to
// anonymous instead of 401ing the whole request.
//
// This is the same-browser lockout. The session cookie is HttpOnly, so the SPA cannot
// clear it; if a dead cookie 401'd every request it rode on, it would 401 POST /login too,
// and the browser could never re-authenticate. Every one of these variants — expired,
// deleted, garbage — must let /login through, because a browser holding any of them is a
// user trying to log back in.
func TestS60_StaleSessionCookieDegradesToAnonymousNotLockout(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	f.user("relogin", pw)

	// A live session, then killed underneath the cookie the browser still holds.
	live, err := f.acct.Login(ctx, "relogin@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	stale := map[string]string{
		"garbage":         "not-a-real-session-id",
		"wrong-shape":     strings.Repeat("a", 43), // plausible shape, never issued
		"deleted-session": live.ID,                 // the row is deleted below
	}

	if _, derr := f.pool.Exec(ctx, `DELETE FROM sessions`); derr != nil {
		t.Fatalf("delete session: %v", derr)
	}

	for name, sid := range stale {
		t.Run("login succeeds carrying a "+name+" cookie", func(t *testing.T) {
			r := f.do(http.MethodPost, "/api/v1/login", withCookie(sid),
				withBody("application/json", loginBody("relogin@ctf.test", pw)))
			// The regression: without the degrade this is 401 — the middleware rejects the whole
			// request on the dead cookie before the login handler ever runs.
			if r.StatusCode == http.StatusUnauthorized {
				t.Fatalf("POST /login with a stale cookie was 401'd — the browser is locked out of re-login")
			}
			if r.StatusCode != http.StatusOK {
				t.Fatalf("POST /login with a stale cookie: %d, want 200", r.StatusCode)
			}
			// The clear the middleware emits for the dead cookie must not clobber the fresh
			// session the login handler sets on the very same response.
			if !hasLiveSessionCookie(r) {
				t.Errorf("login did not return a live session cookie (cookies=%v)", r.Cookies)
			}
		})
	}
}

// A stale cookie grants no more than a fresh anonymous caller gets. The degrade must not
// become a downgrade that reaches a protected route.
func TestS61_StaleCookieReachesNothingAnonymousCannot(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	f.user("noaccess", pw)
	live, err := f.acct.Login(ctx, "noaccess@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, derr := f.pool.Exec(ctx, `DELETE FROM sessions`); derr != nil {
		t.Fatalf("delete session: %v", derr)
	}

	bare := f.do(http.MethodGet, "/api/v1/probe").StatusCode
	stale := f.do(http.MethodGet, "/api/v1/probe", withCookie(live.ID)).StatusCode

	if stale == http.StatusOK {
		t.Errorf("a stale cookie reached a protected route: %d — the degrade leaked access", stale)
	}
	if stale != bare {
		t.Errorf("stale-cookie caller (%d) and fresh-anonymous caller (%d) were treated differently", stale, bare)
	}
}

// The dead cookie is expired off the browser: an anonymous request that still carries a
// session cookie gets a clearing Set-Cookie, and a valid one never does.
func TestS62_DeadCookieIsCleared(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	f.user("clearme", pw)
	live, err := f.acct.Login(ctx, "clearme@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	t.Run("a valid cookie is not cleared", func(t *testing.T) {
		r := f.do(http.MethodGet, "/api/v1/probe", withCookie(live.ID))
		if c := r.cookie(accounts.SessionCookie); c != nil && c.MaxAge < 0 {
			t.Errorf("a valid session cookie was cleared out from under the caller")
		}
	})

	if _, derr := f.pool.Exec(ctx, `DELETE FROM sessions`); derr != nil {
		t.Fatalf("delete session: %v", derr)
	}

	t.Run("a dead cookie is expired", func(t *testing.T) {
		c := f.do(http.MethodGet, "/api/v1/probe", withCookie(live.ID)).cookie(accounts.SessionCookie)
		if c == nil || c.MaxAge >= 0 {
			t.Errorf("a dead session cookie was not expired off the browser (cookie=%v)", c)
		}
	})

	t.Run("no cookie means no Set-Cookie", func(t *testing.T) {
		if c := f.do(http.MethodGet, "/api/v1/probe").cookie(accounts.SessionCookie); c != nil {
			t.Errorf("a request with no cookie was sent a Set-Cookie anyway: %v", c)
		}
	})
}

// A bad bearer token still 401s: only the cookie degrades, never the token. A program
// presenting a dead token can and should be told so.
func TestS63_StaleBearerTokenStill401s(t *testing.T) {
	f := setup(t)
	f.user("tokholder", pw)

	got := f.do(http.MethodPost, "/api/v1/login",
		withToken("flagfish_"+strings.Repeat("a", 64)),
		withBody("application/json", loginBody("tokholder@ctf.test", pw))).StatusCode
	if got != http.StatusUnauthorized {
		t.Errorf("POST /login with a bad bearer token: %d, want 401 — a token is an explicit "+
			"credential and does not degrade the way a browser cookie does", got)
	}
}

// The ban wall is untouched: a banned user holding a VALID session is still walled. The
// degrade only fires on invalid cookies, so a valid one still resolves to a Principal the
// wall reads and refuses.
func TestS64_BanWallStillHoldsForValidSession(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	uid := f.user("banned", pw)
	sess, err := f.acct.Login(ctx, "banned@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	f.ban(uid)

	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode; got != http.StatusForbidden {
		t.Errorf("banned user with a valid session: %d, want 403 — the ban wall must be unaffected", got)
	}
}

// Logout-everywhere survives the degrade. After a password change the old cookie is dead
// (fingerprint mismatch); the holder can re-login in the same browser, but the old cookie
// reaches nothing protected. Same guarantee as before — the thief is out — just no longer
// spelled as a 401 that also blocks the victim's re-login.
func TestS65_PasswordChangeLogoutEverywhereWithSameBrowserRelogin(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	uid := f.user("rotator", pw)
	old, err := f.acct.Login(ctx, "rotator@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(old.ID)).StatusCode; got != http.StatusOK {
		t.Fatalf("the old cookie should work before the change: %d", got)
	}

	const newPW = "an entirely different password"
	if _, _, err := f.acct.ChangePassword(ctx, uid, pw, newPW); err != nil {
		t.Fatalf("change password: %v", err)
	}

	// The old cookie reaches nothing protected — the logout-everywhere guarantee.
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(old.ID)).StatusCode; got == http.StatusOK {
		t.Errorf("the OLD cookie still reached a protected route after the password change: %d", got)
	}

	// …but the same browser, still carrying that old cookie, can log back in.
	r := f.do(http.MethodPost, "/api/v1/login", withCookie(old.ID),
		withBody("application/json", loginBody("rotator@ctf.test", newPW)))
	if r.StatusCode != http.StatusOK {
		t.Errorf("same-browser re-login after a password change: %d, want 200 — the dead cookie "+
			"must not lock the user out of logging back in", r.StatusCode)
	}
	if !hasLiveSessionCookie(r) {
		t.Errorf("re-login did not return a live session cookie (cookies=%v)", r.Cookies)
	}
}

// hasLiveSessionCookie reports whether a response set a real, non-expiring session cookie —
// distinguishing the fresh login cookie from the MaxAge<0 clear the middleware may also emit.
func hasLiveSessionCookie(r resp) bool {
	for _, c := range r.Cookies {
		if c.Name == accounts.SessionCookie && c.Value != "" && c.MaxAge >= 0 {
			return true
		}
	}
	return false
}
