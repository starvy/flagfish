//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

type teamView struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Score     int64  `json:"score"`
	IsCaptain bool   `json:"is_captain"`
	Members   []struct {
		UserID     int64  `json:"user_id"`
		Name       string `json:"name"`
		Captain    bool   `json:"captain"`
		SolveCount int64  `json:"solve_count"`
		Points     int64  `json:"points"`
	} `json:"members"`
}

// newTeamAPI is newAPI in teams mode with the config's user_mode seeded to match: the policy
// layer reads the mode from config, so a route only exists once the config agrees it is teams.
func newTeamAPI(t *testing.T, extra ...[2]string) *apiFix {
	t.Helper()
	return newAPI(t, account.ModeTeams, append([][2]string{{"user_mode", "teams"}}, extra...)...)
}

func decodeTeam(t *testing.T, body []byte) teamView {
	t.Helper()
	var v teamView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode team: %v (%s)", err, body)
	}
	return v
}

// createTeam hits POST /teams and returns the created team.
func (f *apiFix) createTeam(cookie, csrf, name, password string) teamView {
	f.t.Helper()
	res, body := f.do(http.MethodPost, "/api/v1/teams", map[string]any{
		"name": name, "password": password,
	}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("create team %s: status %d: %s", name, res.StatusCode, body)
	}
	return decodeTeam(f.t, body)
}

func TestTeamCreateJoinProfileRoundTrip(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")

	team := f.createTeam(aCookie, aCSRF, "Bit Flippers", "hunter22")
	if !team.IsCaptain {
		t.Fatal("the creator must come back as captain")
	}

	res, body := f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
		"name": "Bit Flippers", "password": "hunter22",
	}, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("join: status %d: %s", res.StatusCode, body)
	}
	if joined := decodeTeam(t, body); joined.IsCaptain {
		t.Fatal("a joiner must not become captain of a captained team")
	}

	// The public profile is anonymous-readable and lists both members with the captain marked.
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/teams/%d", team.ID), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("profile: status %d: %s", res.StatusCode, body)
	}
	profile := decodeTeam(t, body)
	if len(profile.Members) != 2 {
		t.Fatalf("members = %d, want 2: %s", len(profile.Members), body)
	}
	captains := 0
	for _, m := range profile.Members {
		if m.Captain {
			captains++
			if m.Name != "Ada" {
				t.Errorf("captain = %s, want Ada", m.Name)
			}
		}
	}
	if captains != 1 {
		t.Errorf("captains = %d, want exactly 1", captains)
	}

	// GET /me/team agrees with the profile and carries the caller's captain flag.
	res, body = f.do(http.MethodGet, "/api/v1/me/team", nil, withCookie(bCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me/team: status %d: %s", res.StatusCode, body)
	}
	mine := decodeTeam(t, body)
	if mine.ID != team.ID || mine.IsCaptain {
		t.Fatalf("me/team: got id=%d captain=%v, want id=%d captain=false", mine.ID, mine.IsCaptain, team.ID)
	}
}

func TestTeamJoinWrongPassword(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	f.createTeam(aCookie, aCSRF, "Bit Flippers", "hunter22")

	for name, req := range map[string]map[string]any{
		"wrong password": {"name": "Bit Flippers", "password": "wrong"},
		"unknown team":   {"name": "No Such Team", "password": "hunter22"},
	} {
		res, body := f.do(http.MethodPost, "/api/v1/teams/join", req, withCookie(bCookie), withCSRF(bCSRF))
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: status %d, want 403: %s", name, res.StatusCode, body)
		}
	}

	// Bob must still be teamless afterwards.
	if res, _ := f.do(http.MethodGet, "/api/v1/me/team", nil, withCookie(bCookie)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("me/team after failed joins: status %d, want 404", res.StatusCode)
	}
}

func TestTeamRoutesDoNotExistInUsersMode(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	for name, try := range map[string]func() apiResp{
		"create": func() apiResp {
			r, _ := f.do(http.MethodPost, "/api/v1/teams", map[string]any{"name": "x"}, withCookie(cookie), withCSRF(csrf))
			return r
		},
		"join": func() apiResp {
			r, _ := f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{"name": "x"}, withCookie(cookie), withCSRF(csrf))
			return r
		},
		"detail": func() apiResp { r, _ := f.do(http.MethodGet, "/api/v1/teams/1", nil, withCookie(cookie)); return r },
		"mine":   func() apiResp { r, _ := f.do(http.MethodGet, "/api/v1/me/team", nil, withCookie(cookie)); return r },
		"leave": func() apiResp {
			r, _ := f.do(http.MethodPost, "/api/v1/me/team/leave", nil, withCookie(cookie), withCSRF(csrf))
			return r
		},
	} {
		if res := try(); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s in users mode: status %d, want 404 (the route does not exist)", name, res.StatusCode)
		}
	}
}

func TestTeamDuplicateNameConflicts(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	f.createTeam(aCookie, aCSRF, "Bit Flippers", "hunter22")

	res, body := f.do(http.MethodPost, "/api/v1/teams", map[string]any{
		"name": "Bit Flippers", "password": "otherpass",
	}, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate name: status %d, want 409: %s", res.StatusCode, body)
	}
}

func TestTeamJoinRejectedWhenFull(t *testing.T) {
	f := newTeamAPI(t, [2]string{"team_size", "1"})
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	f.createTeam(aCookie, aCSRF, "Bit Flippers", "hunter22")

	res, body := f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
		"name": "Bit Flippers", "password": "hunter22",
	}, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("join full team: status %d, want 403: %s", res.StatusCode, body)
	}
}

func TestTeamCannotJoinAnotherWhileEnrolled(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	f.createTeam(aCookie, aCSRF, "First", "pw-first")
	f.createTeam(bCookie, bCSRF, "Second", "pw-second")

	res, body := f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
		"name": "First", "password": "pw-first",
	}, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("join while enrolled: status %d, want 403: %s", res.StatusCode, body)
	}
	// Creating a second team is refused at the policy gate for the same reason.
	res, body = f.do(http.MethodPost, "/api/v1/teams", map[string]any{
		"name": "Third",
	}, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("create while enrolled: status %d, want 403: %s", res.StatusCode, body)
	}
}

func TestTeamLeavePassesCaptaincyAndBlocksAfterSolves(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	team := f.createTeam(aCookie, aCSRF, "Bit Flippers", "hunter22")

	res, body := f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
		"name": "Bit Flippers", "password": "hunter22",
	}, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("join: status %d: %s", res.StatusCode, body)
	}

	// The captain leaves; the seat passes to Bob rather than dangling.
	res, body = f.do(http.MethodPost, "/api/v1/me/team/leave", nil, withCookie(aCookie), withCSRF(aCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("leave: status %d: %s", res.StatusCode, body)
	}
	res, body = f.do(http.MethodGet, "/api/v1/me/team", nil, withCookie(bCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me/team: status %d: %s", res.StatusCode, body)
	}
	if mine := decodeTeam(t, body); !mine.IsCaptain {
		t.Fatal("captaincy must pass to the remaining member when the captain leaves")
	}

	// Once the team has a solve, nobody leaves. Attribute it to Bob, the sole remaining member.
	var bobID int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE name = 'Bob'`).Scan(&bobID); err != nil {
		t.Fatalf("resolve bob: %v", err)
	}
	ch := f.seedChallenge("pwn1", "pwn", 100)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO solves (challenge_id, user_id, team_id, value) VALUES ($1,$2,$3,100)`,
		ch, bobID, team.ID); err != nil {
		t.Fatalf("seed solve: %v", err)
	}
	res, body = f.do(http.MethodPost, "/api/v1/me/team/leave", nil, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("leave after solve: status %d, want 403: %s", res.StatusCode, body)
	}

	// The profile attributes the solve to the member who scored it.
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/teams/%d", team.ID), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("profile: status %d: %s", res.StatusCode, body)
	}
	profile := decodeTeam(t, body)
	if profile.Score != 100 {
		t.Errorf("team score = %d, want 100", profile.Score)
	}
	if len(profile.Members) != 1 || profile.Members[0].Points != 100 || profile.Members[0].SolveCount != 1 {
		t.Errorf("member attribution wrong: %s", body)
	}
}

func TestTeamHiddenProfileIs404(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	team := f.createTeam(aCookie, aCSRF, "Ghosts", "boo-hunter")

	if _, err := f.pool.Exec(context.Background(),
		`UPDATE teams SET hidden = true WHERE id = $1`, team.ID); err != nil {
		t.Fatalf("hide team: %v", err)
	}
	if res, _ := f.do(http.MethodGet, fmt.Sprintf("/api/v1/teams/%d", team.ID), nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden team profile: status %d, want 404", res.StatusCode)
	}
	// The team itself still sees its own page: hiddenness is a listing predicate, not a lockout.
	if res, _ := f.do(http.MethodGet, "/api/v1/me/team", nil, withCookie(aCookie)); res.StatusCode != http.StatusOK {
		t.Fatalf("own hidden team: status %d, want 200", res.StatusCode)
	}
}
