//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/e2e/client"
)

// TestAdminRequiresAdmin confirms the admin surface is walled off from ordinary users.
func TestAdminRequiresAdmin(t *testing.T) {
	u := mustRegister(t)
	for _, path := range []string{"/config", "/users"} {
		r := u.adminReq(t, http.MethodGet, path, nil)
		if r.code != 403 {
			t.Errorf("GET /admin%s as user = %d, want 403; body=%s", path, r.code, r.body)
		}
		if got := r.problemType(t); got != "admin-required" {
			t.Errorf("GET /admin%s reason = %q, want admin-required", path, got)
		}
	}
}

// TestAdminChallengeFlagHintCRUD exercises the admin catalog editing surface end to end,
// including that a hidden challenge disappears from a player's board and a deleted challenge
// 404s.
func TestAdminChallengeFlagHintCRUD(t *testing.T) {
	ctx := context.Background()
	chal := adminCreateChallenge(t, "crud-"+suffix(), "misc", 100)

	// Update the value.
	var updated struct {
		Value int32 `json:"value"`
	}
	admin.adminReq(t, http.MethodPatch, fmt.Sprintf("/challenges/%d", chal), map[string]any{
		"value": 250,
	}).require(t, http.StatusOK).decode(t, &updated)
	if updated.Value != 250 {
		t.Errorf("updated value = %d, want 250", updated.Value)
	}

	// Flags: add, update, delete.
	flagID := adminAddStaticFlag(t, chal, "flag{crud-a}")
	admin.adminReq(t, http.MethodPatch, fmt.Sprintf("/challenges/%d/flags/%d", chal, flagID), map[string]any{
		"content": "flag{crud-b}",
	}).require(t, http.StatusOK)
	admin.adminReq(t, http.MethodDelete, fmt.Sprintf("/challenges/%d/flags/%d", chal, flagID), nil).require(t, http.StatusNoContent)

	// Hints: add, update, delete.
	hintID := adminAddHint(t, chal, "a hint", 10)
	admin.adminReq(t, http.MethodPatch, fmt.Sprintf("/challenges/%d/hints/%d", chal, hintID), map[string]any{
		"cost": 20,
	}).require(t, http.StatusOK)
	admin.adminReq(t, http.MethodDelete, fmt.Sprintf("/challenges/%d/hints/%d", chal, hintID), nil).require(t, http.StatusNoContent)

	// Hiding removes it from a player's board; showing restores it.
	player := mustRegister(t)
	admin.adminReq(t, http.MethodPut, fmt.Sprintf("/challenges/%d/state", chal), map[string]any{"state": "hidden"}).require(t, http.StatusOK)
	if listContains(t, player, chal) {
		t.Error("a hidden challenge must not appear on a player's board")
	}
	admin.adminReq(t, http.MethodPut, fmt.Sprintf("/challenges/%d/state", chal), map[string]any{"state": "visible"}).require(t, http.StatusOK)
	if !listContains(t, player, chal) {
		t.Error("a visible challenge should appear on the board")
	}

	// Deleting a solve-free challenge succeeds; it then 404s.
	admin.adminReq(t, http.MethodDelete, fmt.Sprintf("/challenges/%d", chal), nil).require(t, http.StatusNoContent)
	gone, err := player.api.ChallengeDetailWithResponse(ctx, chal)
	if err != nil {
		t.Fatalf("detail after delete: %v", err)
	}
	if gone.StatusCode() != http.StatusNotFound {
		t.Errorf("deleted challenge detail = %d, want 404", gone.StatusCode())
	}
}

// TestAdminConfigVisibility flips challenge and score visibility and checks the effect on an
// anonymous caller, then restores the defaults.
func TestAdminConfigVisibility(t *testing.T) {
	ctx := context.Background()
	anon := anonUser()

	// Public challenge visibility: anonymous listing is allowed.
	got := setConfig(t, map[string]any{"challenge_visibility": "public"})
	if got["challenge_visibility"] != "public" {
		t.Fatalf("challenge_visibility = %v, want public", got["challenge_visibility"])
	}
	if code := anonListStatus(t, anon); code != http.StatusOK {
		t.Errorf("anonymous list under public = %d, want 200", code)
	}

	// Private (the default): anonymous listing is walled to login.
	setConfig(t, map[string]any{"challenge_visibility": "private"})
	if code := anonListStatus(t, anon); code != http.StatusForbidden {
		t.Errorf("anonymous list under private = %d, want 403", code)
	}

	// Hidden scores wall the scoreboard for non-admins; restore public afterwards.
	setConfig(t, map[string]any{"score_visibility": "hidden"})
	sb, err := anon.api.ScoreboardWithResponse(ctx, &client.ScoreboardParams{})
	if err != nil {
		t.Fatalf("scoreboard: %v", err)
	}
	if sb.StatusCode() == http.StatusOK {
		t.Error("anonymous scoreboard should be walled when scores are hidden")
	}
	setConfig(t, map[string]any{"score_visibility": "public"})
}

// TestAdminBanWallCoversToken bans a user holding a valid API token and confirms the ban wall
// blocks the token — the property a session-only ban would miss.
func TestAdminBanWallCoversToken(t *testing.T) {
	ctx := context.Background()
	victim := mustRegister(t)

	tok, err := victim.api.CreateTokenWithResponse(ctx, client.CreateTokenInputBody{})
	if err != nil || tok.JSON200 == nil {
		t.Fatalf("create token: %v (status %d)", err, tok.StatusCode())
	}
	bearer := tokenClient(t, tok.JSON200.Token)
	bearer.publicReq(t, http.MethodGet, "/me", nil).require(t, http.StatusOK)

	// Ban, then the token is walled.
	admin.adminReq(t, http.MethodPut, fmt.Sprintf("/users/%d/ban", victim.userID), map[string]any{"banned": true}).require(t, http.StatusOK)
	walled := bearer.publicReq(t, http.MethodGet, "/me", nil)
	if walled.code != http.StatusForbidden || walled.problemType(t) != "banned" {
		t.Errorf("banned token /me = %d (%s), want 403 banned; body=%s", walled.code, walled.problemType(t), walled.body)
	}

	// Unban restores access.
	admin.adminReq(t, http.MethodPut, fmt.Sprintf("/users/%d/ban", victim.userID), map[string]any{"banned": false}).require(t, http.StatusOK)
	bearer.publicReq(t, http.MethodGet, "/me", nil).require(t, http.StatusOK)
}

// TestAdminRolePromoteDemote promotes a user to admin (granting the admin surface) and demotes
// them again (revoking it), reading the change through the API on the very next request.
func TestAdminRolePromoteDemote(t *testing.T) {
	u := mustRegister(t)

	// Before promotion: no admin surface.
	u.adminReq(t, http.MethodGet, "/config", nil).require(t, http.StatusForbidden)

	admin.adminReq(t, http.MethodPut, fmt.Sprintf("/users/%d/role", u.userID), map[string]any{"role": "admin"}).require(t, http.StatusOK)
	// The role is read fresh each request, so the same session is now an admin.
	u.adminReq(t, http.MethodGet, "/config", nil).require(t, http.StatusOK)

	admin.adminReq(t, http.MethodPut, fmt.Sprintf("/users/%d/role", u.userID), map[string]any{"role": "user"}).require(t, http.StatusOK)
	u.adminReq(t, http.MethodGet, "/config", nil).require(t, http.StatusForbidden)
}

func anonListStatus(t *testing.T, u *user) int {
	t.Helper()
	r, err := u.api.ListChallengesWithResponse(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return r.StatusCode()
}

func listContains(t *testing.T, u *user, id int64) bool {
	t.Helper()
	list, err := u.api.ListChallengesWithResponse(context.Background())
	if err != nil || list.JSON200 == nil {
		t.Fatalf("list: %v (status %d)", err, list.StatusCode())
	}
	if list.JSON200.Challenges == nil {
		return false
	}
	for _, it := range *list.JSON200.Challenges {
		if it.Id == id {
			return true
		}
	}
	return false
}
