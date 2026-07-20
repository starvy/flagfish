//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// playerTags reads the tag list off the player detail view — the surface the attach exists for.
func (f *apiFix) playerTags(chID int64, cookie string) []string {
	f.t.Helper()
	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", chID), nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("player detail: got %d (%s)", res.StatusCode, body)
	}
	var out struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decode detail tags: %v (%s)", err, body)
	}
	return out.Tags
}

// TestAdminTagAttachDetach: tags could only be renamed, merged or destroyed board-wide; attaching
// one to a challenge was importer-only. The sub-resource closes that gap, constraint-mapped: the
// duplicate is the UNIQUE violation, the missing challenge is the FK, and there are no pre-checks.
func TestAdminTagAttachDetach(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	chID := f.adminChallenge("Tagged", 100, auth...)
	playerCookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")
	tagsPath := fmt.Sprintf("/api/v1/admin/challenges/%d/tags", chID)

	// Attach lands and the player detail shows it.
	res, body := f.do(http.MethodPost, tagsPath, map[string]any{"value": "web"}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("attach: got %d, want 201 (%s)", res.StatusCode, body)
	}
	var tag struct {
		ChallengeID int64  `json:"challenge_id"`
		Value       string `json:"value"`
	}
	if err := json.Unmarshal(body, &tag); err != nil {
		t.Fatalf("decode tag: %v (%s)", err, body)
	}
	if tag.ChallengeID != chID || tag.Value != "web" {
		t.Fatalf("tag echo = %+v, want challenge %d / web", tag, chID)
	}
	if got := f.playerTags(chID, playerCookie); len(got) != 1 || got[0] != "web" {
		t.Fatalf("player tags = %v, want [web]", got)
	}

	// The duplicate is a conflict, straight from UNIQUE(challenge_id, value).
	res, body = f.do(http.MethodPost, tagsPath, map[string]any{"value": "web"}, auth...)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate attach: got %d, want 409 (%s)", res.StatusCode, body)
	}

	// A missing challenge is the FK violation, mapped to 404.
	res, body = f.do(http.MethodPost, "/api/v1/admin/challenges/999999/tags",
		map[string]any{"value": "web"}, auth...)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("attach to missing challenge: got %d, want 404 (%s)", res.StatusCode, body)
	}

	// Detach removes it from the player view; a second detach finds nothing.
	res, body = f.do(http.MethodDelete, tagsPath+"/web", nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("detach: got %d, want 204 (%s)", res.StatusCode, body)
	}
	if got := f.playerTags(chID, playerCookie); len(got) != 0 {
		t.Fatalf("player tags after detach = %v, want none", got)
	}
	res, body = f.do(http.MethodDelete, tagsPath+"/web", nil, auth...)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("re-detach: got %d, want 404 (%s)", res.StatusCode, body)
	}

	// Both the attach and the detach were audited against the admin.
	if got := f.auditCount("tags", "INSERT", adminID); got == 0 {
		t.Error("no INSERT audit row for the tag attach")
	}
	if got := f.auditCount("tags", "DELETE", adminID); got == 0 {
		t.Error("no DELETE audit row for the tag detach")
	}
}
