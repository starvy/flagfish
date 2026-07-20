//go:build integration

package integration

import (
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
