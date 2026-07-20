//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

type teamListBody struct {
	Teams []struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		Email       *string `json:"email"`
		Hidden      bool    `json:"hidden"`
		Banned      bool    `json:"banned"`
		MemberCount int64   `json:"member_count"`
	} `json:"teams"`
	Total int64 `json:"total"`
}

func TestAdminTeamListAndDetail(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	alpha := f.seedTeam("alpha") // seedTeam stamps alpha@team.test
	f.seedTeam("beta")
	f.joinTeam("root@example.com", alpha)

	// list: both teams, total agrees with the page
	res, body := f.do(http.MethodGet, "/api/v1/admin/teams", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: got %d (%s)", res.StatusCode, body)
	}
	var list teamListBody
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if list.Total != 2 || len(list.Teams) != 2 {
		t.Errorf("list: total=%d len=%d, want 2/2", list.Total, len(list.Teams))
	}

	// search by email — the one field only the admin surface may search
	res, body = f.do(http.MethodGet, "/api/v1/admin/teams?q=alpha%40&field=email", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("search: got %d (%s)", res.StatusCode, body)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(list.Teams) != 1 || list.Teams[0].ID != alpha {
		t.Errorf("email search: got %+v, want exactly team %d", list.Teams, alpha)
	}

	// detail carries the member count
	res, body = f.do(http.MethodGet, "/api/v1/admin/teams/"+itoa(alpha), nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("detail: got %d (%s)", res.StatusCode, body)
	}
	var detail struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		MemberCount int64  `json:"member_count"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.ID != alpha || detail.Name != "alpha" || detail.MemberCount != 1 {
		t.Errorf("detail = %+v, want id=%d name=alpha member_count=1", detail, alpha)
	}

	// a missing team is a 404, not an empty row
	res, _ = f.do(http.MethodGet, "/api/v1/admin/teams/424242", nil, auth...)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("missing team: got %d, want 404", res.StatusCode)
	}
}

func TestAdminTeamCreateAndPatch(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// create — captainless, with a join password and profile data
	res, body := f.do(http.MethodPost, "/api/v1/admin/teams", map[string]any{
		"name": "provisioned", "password": "join-me-maybe",
		"email": "prov@ctf.test", "country": "CZ",
	}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (%s)", res.StatusCode, body)
	}
	teamID := decodeID(t, body)

	var captain *int64
	var storedHash *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT captain_id, password_hash FROM teams WHERE id = $1`, teamID).Scan(&captain, &storedHash); err != nil {
		t.Fatalf("read created team: %v", err)
	}
	if captain != nil {
		t.Errorf("admin-created team has captain %d, want captainless (adopted on first join)", *captain)
	}
	if storedHash == nil || *storedHash == "join-me-maybe" {
		t.Error("the join password was not stored hashed")
	}

	// duplicate name → 409 (teams_name_uniq arbitrates, not a prior SELECT)
	res, body = f.do(http.MethodPost, "/api/v1/admin/teams", map[string]any{"name": "provisioned"}, auth...)
	if res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate name: got %d, want 409 (%s)", res.StatusCode, body)
	}

	// patch: set website, clear country (explicit null), rename
	res, body = f.do(http.MethodPatch, "/api/v1/admin/teams/"+itoa(teamID), map[string]any{
		"name": "provisioned-2", "website": "https://prov.example", "country": nil,
	}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch: got %d (%s)", res.StatusCode, body)
	}
	var got struct {
		Name    string  `json:"name"`
		Email   *string `json:"email"`
		Website *string `json:"website"`
		Country *string `json:"country"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if got.Name != "provisioned-2" || got.Website == nil || *got.Website != "https://prov.example" {
		t.Errorf("patch result: %+v", got)
	}
	if got.Country != nil {
		t.Errorf("explicit null did not clear country: %v", *got.Country)
	}
	if got.Email == nil || *got.Email != "prov@ctf.test" {
		t.Errorf("an omitted key did not keep its value: email = %v", got.Email)
	}

	// teams_email_uniq is case-insensitive; a PATCH onto another team's address is a 409
	other := f.seedTeam("other") // other@team.test
	res, body = f.do(http.MethodPatch, "/api/v1/admin/teams/"+itoa(other),
		map[string]any{"email": "PROV@ctf.test"}, auth...)
	if res.StatusCode != http.StatusConflict {
		t.Errorf("email collision: got %d, want 409 (%s)", res.StatusCode, body)
	}

	// every write above was audited against the acting admin, in the same transaction
	if n := f.auditCount("teams", "INSERT", adminID); n == 0 {
		t.Error("no INSERT audit row for the created team")
	}
	if n := f.auditCount("teams", "UPDATE", adminID); n == 0 {
		t.Error("no UPDATE audit row for the patched team")
	}
}
