//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

func liveEmail(t *testing.T, f *apiFix, userID int64) (email string, pending *string, verified bool) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT email, pending_email, verified FROM users WHERE id = $1`, userID).
		Scan(&email, &pending, &verified); err != nil {
		t.Fatalf("read user %d: %v", userID, err)
	}
	return email, pending, verified
}

// TestChangeEmailReverifies is the core property: the live email does not move until a token delivered
// to the NEW address is confirmed. Deleting the confirm step (flipping on request) fails this test,
// because the "still bob before confirm" assertions would break.
func TestChangeEmailReverifies(t *testing.T) {
	f, mail := newEmailAPI(t, account.ModeUsers, [2]string{"ctf_name", "Testik"})

	const old, next = "bob@ctf.test", "newbob@ctf.test"
	cookie, csrf := f.register("Bob", old, "hunter2pass")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}
	bobID := f.userID(old)

	// Request the change: the token is mailed to the NEW address, not the old one.
	res, body := f.do(http.MethodPatch, "/api/v1/me/email", map[string]any{"email": next}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("change email: %d (%s)", res.StatusCode, body)
	}
	m := mail.waitMail(t)
	if m.to != next {
		t.Fatalf("confirmation mailed to %q, want %q", m.to, next)
	}
	token := tokenFrom(t, m)

	// Before confirming: live email unchanged, pending set, and the new address does NOT authenticate.
	if email, pending, _ := liveEmail(t, f, bobID); email != old || pending == nil || *pending != next {
		t.Fatalf("pre-confirm state: email=%q pending=%v, want email=%q pending=%q", email, pending, old, next)
	}
	var me struct {
		Email   string  `json:"email"`
		Pending *string `json:"pending_email"`
	}
	if _, mb := f.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie)); json.Unmarshal(mb, &me) == nil {
		if me.Email != old || me.Pending == nil || *me.Pending != next {
			t.Fatalf("/me pre-confirm: %+v", me)
		}
	}
	if res, _ := f.do(http.MethodPost, "/api/v1/login", map[string]any{"email": next, "password": "hunter2pass"}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login with unconfirmed new email: %d, want 401", res.StatusCode)
	}

	// A bogus token is refused with an indistinguishable 400.
	if res, _ := f.do(http.MethodPost, "/api/v1/verify/email-change", map[string]any{"token": "deadbeef"}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("confirm bogus token: %d, want 400", res.StatusCode)
	}

	// Confirm: the address flips, becomes verified, and pending clears.
	if res, cb := f.do(http.MethodPost, "/api/v1/verify/email-change", map[string]any{"token": token}); res.StatusCode != http.StatusOK {
		t.Fatalf("confirm change: %d (%s)", res.StatusCode, cb)
	}
	if email, pending, verified := liveEmail(t, f, bobID); email != next || pending != nil || !verified {
		t.Fatalf("post-confirm state: email=%q pending=%v verified=%v", email, pending, verified)
	}
	if res, _ := f.do(http.MethodPost, "/api/v1/login", map[string]any{"email": next, "password": "hunter2pass"}); res.StatusCode != http.StatusOK {
		t.Fatalf("login with new email after confirm: %d, want 200", res.StatusCode)
	}
	if res, _ := f.do(http.MethodPost, "/api/v1/login", map[string]any{"email": old, "password": "hunter2pass"}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login with old email after confirm: %d, want 401", res.StatusCode)
	}
	// Single-use: the same token is spent.
	if res, _ := f.do(http.MethodPost, "/api/v1/verify/email-change", map[string]any{"token": token}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("re-confirm spent token: %d, want 400", res.StatusCode)
	}
}

// TestChangeEmailTaken proves a taken address is a loud conflict and queues no mail.
func TestChangeEmailTaken(t *testing.T) {
	f, mail := newEmailAPI(t, account.ModeUsers)

	f.register("Carol", "carol@ctf.test", "hunter2pass")
	cookie, csrf := f.register("Bob", "bob@ctf.test", "hunter2pass")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// Someone else's live address — case-insensitively — is a conflict.
	if res, body := f.do(http.MethodPatch, "/api/v1/me/email", map[string]any{"email": "CAROL@ctf.test"}, auth...); res.StatusCode != http.StatusConflict {
		t.Fatalf("change to taken email: %d, want 409 (%s)", res.StatusCode, body)
	}
	// The caller's own current address is a no-op conflict, not a needless token.
	if res, _ := f.do(http.MethodPatch, "/api/v1/me/email", map[string]any{"email": "bob@ctf.test"}, auth...); res.StatusCode != http.StatusConflict {
		t.Fatalf("change to own email: %d, want 409", res.StatusCode)
	}
	mail.expectNoMail(t)
}

// TestEmailChangeTokenIsScoped proves the security seam: only a token of the email-change purpose,
// delivered to the new address, can flip the live email. A stale registration verify token — sent to
// the OLD address — verifies the account but must never complete the change.
func TestEmailChangeTokenIsScoped(t *testing.T) {
	f, mail := newEmailAPI(t, account.ModeUsers, [2]string{"verify_emails", "true"})

	const old, next = "dave@ctf.test", "newdave@ctf.test"
	cookie, csrf := f.register("Dave", old, "hunter2pass")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}
	daveID := f.userID(old)

	regToken := tokenFrom(t, mail.waitMail(t)) // registration verify, mailed to the OLD address

	if res, _ := f.do(http.MethodPatch, "/api/v1/me/email", map[string]any{"email": next}, auth...); res.StatusCode != http.StatusOK {
		t.Fatalf("change email: %d", res.StatusCode)
	}
	changeToken := tokenFrom(t, mail.waitMail(t)) // email-change, mailed to the NEW address

	// Confirming the registration token verifies the account but leaves the change pending: the old
	// address's token has no power over the new address.
	if res, _ := f.do(http.MethodPost, "/api/v1/verify/confirm", map[string]any{"token": regToken}); res.StatusCode != http.StatusOK {
		t.Fatalf("confirm registration token: %d", res.StatusCode)
	}
	if email, pending, verified := liveEmail(t, f, daveID); email != old || pending == nil || *pending != next || !verified {
		t.Fatalf("after registration verify: email=%q pending=%v verified=%v — a verify token must not flip the address", email, pending, verified)
	}

	// The email-change token is what completes the change.
	if res, _ := f.do(http.MethodPost, "/api/v1/verify/email-change", map[string]any{"token": changeToken}); res.StatusCode != http.StatusOK {
		t.Fatalf("confirm change token: %d", res.StatusCode)
	}
	if email, pending, _ := liveEmail(t, f, daveID); email != next || pending != nil {
		t.Fatalf("after change confirm: email=%q pending=%v", email, pending)
	}
}

func TestChangeDisplayName(t *testing.T) {
	f := newAPI(t, account.ModeUsers)

	f.register("Alice", "alice@ctf.test", "correct-horse-battery")
	cookie, csrf := f.register("Bob", "bob@ctf.test", "correct-horse-battery")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPatch, "/api/v1/me/name", map[string]any{"name": "Bobby Tables"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("change name: %d (%s)", res.StatusCode, body)
	}
	var me struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &me); err != nil || me.Name != "Bobby Tables" {
		t.Fatalf("name did not round-trip: %+v (%v)", me, err)
	}

	// A display name is deliberately not unique: colliding with another account's name is allowed,
	// so there is no 409 to assert — the write simply succeeds.
	if res, _ := f.do(http.MethodPatch, "/api/v1/me/name", map[string]any{"name": "Alice"}, auth...); res.StatusCode != http.StatusOK {
		t.Fatalf("change to an existing display name: %d, want 200 (names are not unique)", res.StatusCode)
	}
}
