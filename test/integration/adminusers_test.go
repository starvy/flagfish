//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

func TestAdminUserDetailPatchAndHide(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	f.register("player", "player@example.com", "correct-horse-battery")
	uid := f.userID("player@example.com")

	// detail
	res, body := f.do(http.MethodGet, "/api/v1/admin/users/"+itoa(uid), nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get user: %d (%s)", res.StatusCode, body)
	}
	var u struct {
		ID      int64   `json:"id"`
		Name    string  `json:"name"`
		Email   string  `json:"email"`
		Hidden  bool    `json:"hidden"`
		Website *string `json:"website"`
		Country *string `json:"country"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatalf("decode user: %v", err)
	}
	if u.ID != uid || u.Email != "player@example.com" {
		t.Errorf("detail = %+v", u)
	}

	// profile patch: set website + country, then clear the website with an explicit null
	res, body = f.do(http.MethodPatch, "/api/v1/admin/users/"+itoa(uid), map[string]any{
		"name": "player-renamed", "website": "https://p.example", "country": "CZ",
	}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d (%s)", res.StatusCode, body)
	}
	res, body = f.do(http.MethodPatch, "/api/v1/admin/users/"+itoa(uid),
		map[string]any{"website": nil}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear patch: %d (%s)", res.StatusCode, body)
	}
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatalf("decode patched: %v", err)
	}
	if u.Name != "player-renamed" || u.Website != nil || u.Country == nil || *u.Country != "CZ" {
		t.Errorf("patched user = %+v, want renamed, website cleared, country kept", u)
	}

	// hide toggle
	res, body = f.do(http.MethodPut, "/api/v1/admin/users/"+itoa(uid)+"/hidden",
		map[string]any{"hidden": true}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hide: %d (%s)", res.StatusCode, body)
	}
	var hidden bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT hidden FROM users WHERE id = $1`, uid).Scan(&hidden); err != nil {
		t.Fatalf("read hidden: %v", err)
	}
	if !hidden {
		t.Error("PUT hidden=true did not stick")
	}

	// the list search finds the user by country
	res, body = f.do(http.MethodGet, "/api/v1/admin/users?q=CZ&field=country", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("search: %d (%s)", res.StatusCode, body)
	}
	var list struct {
		Users []struct {
			ID int64 `json:"id"`
		} `json:"users"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Users) != 1 || list.Users[0].ID != uid {
		t.Errorf("country search = %+v, want exactly user %d", list.Users, uid)
	}

	// every mutation audited against the actor, in the same transaction
	if n := f.auditCount("users", "UPDATE", adminID); n == 0 {
		t.Error("no UPDATE audit rows for the user writes")
	}
}

// The forced-change lifecycle over the real HTTP surface: force kills the sessions and raises the
// wall; the exempt password-change endpoint is the exit; the change lowers the wall for good.
func TestAdminForcePasswordChangeLifecycle(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	adminCookie, adminCSRF, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(adminCookie), withCSRF(adminCSRF)}

	victimCookie, _ := f.register("victim", "victim@example.com", "correct-horse-battery")
	uid := f.userID("victim@example.com")

	res, body := f.do(http.MethodPut, "/api/v1/admin/users/"+itoa(uid)+"/force-password-change", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("force: %d (%s)", res.StatusCode, body)
	}

	// The credential is suspect, so the sessions died with the flag — in one transaction.
	if n := f.sessionCount(uid); n != 0 {
		t.Errorf("victim still holds %d sessions after the force", n)
	}
	res, _ = f.do(http.MethodGet, "/api/v1/me", nil, withCookie(victimCookie))
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("old cookie after force: %d, want 401", res.StatusCode)
	}

	// Login works (exempt), but everything else is walled…
	cookie, csrf := f.login("victim@example.com", "correct-horse-battery")
	res, body = f.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie))
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("/me while forced: %d, want 403 (%s)", res.StatusCode, body)
	}

	// …except the exit.
	res, body = f.do(http.MethodPost, "/api/v1/me/password", map[string]any{
		"current_password": "correct-horse-battery", "new_password": "a whole new passphrase",
	}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("password change while forced: %d, want 200 (%s)", res.StatusCode, body)
	}
	fresh := sessionCookie(res)
	if fresh == "" {
		t.Fatal("no session minted by the change")
	}
	res, _ = f.do(http.MethodGet, "/api/v1/me", nil, withCookie(fresh))
	if res.StatusCode != http.StatusOK {
		t.Errorf("/me after the change: %d, want 200 — the wall did not come down", res.StatusCode)
	}

	// The force itself was audited against the acting admin.
	if n := f.auditCount("users", "UPDATE", adminID); n == 0 {
		t.Error("no UPDATE audit row for the force")
	}
}
