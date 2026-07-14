//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/e2e/client"
)

// TestTeamsUnavailableInUserMode confirms the team endpoints do not exist in users mode: the
// policy answers 404 (the route is absent in this mode), not 403.
func TestTeamsUnavailableInUserMode(t *testing.T) {
	if userMode != "users" {
		t.Skipf("instance is in %q mode", userMode)
	}
	u := mustRegister(t)
	r := u.publicReq(t, http.MethodPost, "/teams", map[string]any{"name": "ghost-" + suffix()})
	if r.code != http.StatusNotFound {
		t.Errorf("create-team in users mode = %d, want 404; body=%s", r.code, r.body)
	}
}

// TestTeamsLifecycle runs the teams-mode enrollment flow: a captain creates a team, a second
// player joins it with the password, membership is reflected on both sides, and leaving works.
// It requires an instance booted in teams mode (E2E_USER_MODE=teams).
func TestTeamsLifecycle(t *testing.T) {
	if userMode != "teams" {
		t.Skip("requires a teams-mode instance (set E2E_USER_MODE=teams and boot with user_mode=teams)")
	}
	ctx := context.Background()

	captain := mustRegister(t)
	teamName := "team-" + suffix()
	teamPass := "join-secret"

	created, err := captain.api.CreateTeamWithResponse(ctx, client.CreateTeamInputBody{
		Name: teamName, Password: ptr(teamPass),
	})
	if err != nil || created.JSON200 == nil {
		t.Fatalf("create-team: %v (status %d, body %s)", err, created.StatusCode(), created.Body)
	}
	teamID := created.JSON200.Id

	member := mustRegister(t)
	joined, err := member.api.JoinTeamWithResponse(ctx, client.JoinTeamInputBody{
		Name: teamName, Password: ptr(teamPass),
	})
	if err != nil || joined.JSON200 == nil {
		t.Fatalf("join-team: %v (status %d, body %s)", err, joined.StatusCode(), joined.Body)
	}

	// Both members are on the team.
	mine, err := member.api.MyTeamWithResponse(ctx)
	if err != nil || mine.JSON200 == nil {
		t.Fatalf("my-team: %v (status %d)", err, mine.StatusCode())
	}
	if mine.JSON200.Id != teamID {
		t.Errorf("my-team id = %d, want %d", mine.JSON200.Id, teamID)
	}
	if mine.JSON200.Members == nil || len(*mine.JSON200.Members) != 2 {
		t.Errorf("team should have two members, got %v", mine.JSON200.Members)
	}

	// Public team detail is reachable.
	detail, err := captain.api.TeamDetailWithResponse(ctx, teamID)
	if err != nil || detail.JSON200 == nil {
		t.Fatalf("team-detail: %v (status %d)", err, detail.StatusCode())
	}

	// A member with no solves can leave.
	left, err := member.api.LeaveTeamWithResponse(ctx)
	if err != nil || left.JSON200 == nil {
		t.Fatalf("leave-team: %v (status %d, body %s)", err, left.StatusCode(), left.Body)
	}
}
