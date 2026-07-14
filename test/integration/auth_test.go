//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

func TestRegisterThenMe(t *testing.T) {
	f := newAPI(t, account.ModeUsers)

	cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, body := f.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me: status %d: %s", res.StatusCode, body)
	}
	var me struct {
		UserID  int64  `json:"user_id"`
		Email   string `json:"email"`
		IsAdmin bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("decode me: %v (%s)", err, body)
	}
	if me.Email != "ada@ctf.test" {
		t.Fatalf("me email = %q, want ada@ctf.test", me.Email)
	}
	if me.UserID == 0 {
		t.Fatalf("me user_id = 0, want the first user's id")
	}
	if me.IsAdmin {
		t.Fatalf("first self-registered user must not be admin")
	}
}

func TestLoginSucceeds(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	f.register("Ada", "ada@ctf.test", "correct horse battery")

	cookie, _ := f.login("ada@ctf.test", "correct horse battery")
	res, body := f.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me after login: status %d: %s", res.StatusCode, body)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, _ := f.do(http.MethodPost, "/api/v1/login", map[string]any{
		"email": "ada@ctf.test", "password": "wrong",
	})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", res.StatusCode)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, _ := f.do(http.MethodPost, "/api/v1/register", map[string]any{
		"name": "Imposter", "email": "ADA@ctf.test", "password": "another password",
	})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 on duplicate (case-insensitive) email, got %d", res.StatusCode)
	}
}

func TestMeRequiresAuth(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	res, _ := f.do(http.MethodGet, "/api/v1/me", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("me without a session: want 403, got %d", res.StatusCode)
	}
}

func TestAnonymousWritesAndTokensForbidden(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chID := f.seedChallenge("Warmup", "misc", 100)
	f.seedFlag(chID, "flag{correct}")

	// A submit without a credential is denied by the policy gate, not merely unauthenticated.
	res, body := f.do(http.MethodPost,
		fmt.Sprintf("/api/v1/challenges/%d/attempt", chID),
		map[string]any{"flag": "flag{correct}"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous attempt: want 403, got %d: %s", res.StatusCode, body)
	}

	res, _ = f.do(http.MethodGet, "/api/v1/tokens", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous list tokens: want 403, got %d", res.StatusCode)
	}
}

func TestChangePassword(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	old, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// Wrong current password is rejected.
	res, _ := f.do(http.MethodPost, "/api/v1/me/password",
		map[string]any{"current_password": "nope", "new_password": "brand new secret"},
		withCookie(old), withCSRF(csrf))
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong current password: want 401, got %d", res.StatusCode)
	}

	// A correct change mints a fresh session and kills the old one.
	res, body := f.do(http.MethodPost, "/api/v1/me/password",
		map[string]any{"current_password": "correct horse battery", "new_password": "brand new secret"},
		withCookie(old), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("change password: status %d: %s", res.StatusCode, body)
	}
	newCookie := sessionCookie(res)
	if newCookie == "" || newCookie == old {
		t.Fatalf("change password must mint a new session cookie, got %q", newCookie)
	}

	res, _ = f.do(http.MethodGet, "/api/v1/me", nil, withCookie(newCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("new session after password change: want 200, got %d", res.StatusCode)
	}
	// The old cookie's pw_fingerprint no longer matches, so it reads as an invalid credential (401).
	res, _ = f.do(http.MethodGet, "/api/v1/me", nil, withCookie(old))
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old cookie after password change must be dead: want 401, got %d", res.StatusCode)
	}
}

func TestLogoutClearsSession(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, body := f.do(http.MethodPost, "/api/v1/logout", nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("logout: status %d: %s", res.StatusCode, body)
	}
	// The old cookie is dead: the session row is gone, so presenting it is an invalid credential (401),
	// distinct from presenting none at all (403).
	res, _ = f.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie))
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me after logout: want 401, got %d", res.StatusCode)
	}
}

func TestLogoutNeedsCSRF(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, _ := f.do(http.MethodPost, "/api/v1/logout", nil, withCookie(cookie))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie write without CSRF must be 403, got %d", res.StatusCode)
	}
}
