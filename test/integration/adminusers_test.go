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
