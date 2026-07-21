//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
)

// teamProfileView decodes the public team page including its solve history.
type teamProfileView struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Score  *int64 `json:"score"`
	Solves []struct {
		ChallengeID   int64  `json:"challenge_id"`
		ChallengeName string `json:"challenge_name"`
		Category      string `json:"category"`
		Value         int32  `json:"value"`
		Date          string `json:"date"`
	} `json:"solves"`
}

func decodeTeamProfile(t *testing.T, body []byte) teamProfileView {
	t.Helper()
	var v teamProfileView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode team profile: %v (%s)", err, body)
	}
	return v
}

func (v teamProfileView) solveNames() []string {
	names := make([]string, len(v.Solves))
	for i, s := range v.Solves {
		names[i] = s.ChallengeName
	}
	return names
}

// TestTeamProfileSolveList proves the public team page carries the team's solve history, newest
// first, and that a hidden challenge the team solved is scored but never named. Without the solves
// field on the team body this decodes to an empty list and the first assertion fails.
func TestTeamProfileSolveList(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	team := f.createTeam(aCookie, aCSRF, "Bit Flippers", "hunter22")
	ada := f.userIDByEmail("ada@ctf.test")

	older := f.seedChallenge("Reversing", "rev", 100)
	newer := f.seedChallenge("Pwning", "pwn", 300)
	f.seedDatedTeamSolve(older, ada, team.ID, 100, time.Now().Add(-2*time.Hour))
	f.seedDatedTeamSolve(newer, ada, team.ID, 300, time.Now().Add(-time.Hour))

	// A hidden challenge the team also solved: its 250 counts, its name must not leak.
	hidden := f.seedHiddenChallenge("SECRET-CHAL", "hidden-cat", 250)
	f.seedDatedTeamSolve(hidden, ada, team.ID, 250, time.Now())

	prof := decodeTeamProfile(t, f.get(t, fmt.Sprintf("/api/v1/teams/%d", team.ID)))

	if prof.Score == nil || *prof.Score != 650 {
		t.Fatalf("team score = %v, want 650 (100 + 300 + 250 hidden)", prof.Score)
	}
	// Only the two visible challenges, newest first.
	if got := prof.solveNames(); len(got) != 2 || got[0] != "Pwning" || got[1] != "Reversing" {
		t.Fatalf("solves = %v, want [Pwning Reversing] (newest first, hidden excluded)", got)
	}
	for _, s := range prof.Solves {
		if s.ChallengeName == "SECRET-CHAL" {
			t.Fatal("team solve list leaked a hidden challenge")
		}
	}
}

// TestTeamProfileSolveListFreezeClamps proves the solve history is clamped to the freeze for a
// non-admin, exactly as the score and the per-member breakdown are. Drop the cutoff from the query
// and the post-freeze solve reappears in an anonymous read.
func TestTeamProfileSolveListFreezeClamps(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newTeamAPI(t, [2]string{"freeze", strconv.FormatInt(freeze.Unix(), 10)})
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	team := f.createTeam(aCookie, aCSRF, "Bit Flippers", "hunter22")
	ada := f.userIDByEmail("ada@ctf.test")

	early := f.seedChallenge("Early", "rev", 100)
	late := f.seedChallenge("Late", "rev", 500)
	f.seedDatedTeamSolve(early, ada, team.ID, 100, freeze.Add(-time.Hour))
	f.seedDatedTeamSolve(late, ada, team.ID, 500, time.Now())

	prof := decodeTeamProfile(t, f.get(t, fmt.Sprintf("/api/v1/teams/%d", team.ID)))
	names := prof.solveNames()
	if !contains(names, "Early") {
		t.Fatalf("pre-freeze team solve missing from the frozen list: %v", names)
	}
	if contains(names, "Late") {
		t.Fatalf("post-freeze team solve leaked through the freeze: %v", names)
	}
}

// TestTeamProfileSolveListRedactedWhenScoresHidden proves the solve list rides score_visibility: when
// scores are withheld the list comes back empty (omission, not partial), the same gate the headline
// score and the user profile's solve list obey.
func TestTeamProfileSolveListRedactedWhenScoresHidden(t *testing.T) {
	f := newTeamAPI(t, [2]string{"score_visibility", "hidden"})
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	team := f.createTeam(aCookie, aCSRF, "Bit Flippers", "hunter22")
	ada := f.userIDByEmail("ada@ctf.test")

	ch := f.seedChallenge("Reversing", "rev", 100)
	f.seedDatedTeamSolve(ch, ada, team.ID, 100, time.Now())

	prof := decodeTeamProfile(t, f.get(t, fmt.Sprintf("/api/v1/teams/%d", team.ID)))
	if prof.Score != nil {
		t.Fatalf("score = %v, want null (withheld under score_visibility=hidden)", *prof.Score)
	}
	if len(prof.Solves) != 0 {
		t.Fatalf("solve list leaked while scores are hidden: %+v", prof.Solves)
	}
}

// TestTeamProfileHiddenTeamLeaksNothing proves a hidden team's solve history never leaves the server:
// the whole profile 404s, so there is no body to carry solves at all.
func TestTeamProfileHiddenTeamLeaksNothing(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	team := f.createTeam(aCookie, aCSRF, "Ghosts", "boo-hunter")
	ada := f.userIDByEmail("ada@ctf.test")

	ch := f.seedChallenge("Reversing", "rev", 100)
	f.seedDatedTeamSolve(ch, ada, team.ID, 100, time.Now())

	if _, err := f.pool.Exec(context.Background(),
		`UPDATE teams SET hidden = true WHERE id = $1`, team.ID); err != nil {
		t.Fatalf("hide team: %v", err)
	}
	if res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/teams/%d", team.ID), nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden team profile: status %d, want 404 (%s)", res.StatusCode, body)
	}
}

// publicProfileView decodes the public user page including its public custom-field answers.
type publicProfileView struct {
	ID     int64 `json:"id"`
	Fields []struct {
		ID        int64           `json:"id"`
		Name      string          `json:"name"`
		FieldType string          `json:"field_type"`
		Value     json.RawMessage `json:"value"`
	} `json:"fields"`
}

// TestPublicProfilePublicFieldShows proves a field the admin flagged public surfaces on the public
// profile, and a private field never does — even though both are answered on the account. Without
// the fields projection on the profile body this decodes to an empty list and the public assertion
// fails.
func TestPublicProfilePublicFieldShows(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	publicField := f.createField(auth, map[string]any{
		"name": "School", "applies_to": "user", "field_type": "text", "public": true, "editable": true,
	})
	privateField := f.createField(auth, map[string]any{
		"name": "Badge", "applies_to": "user", "field_type": "text", "public": false, "editable": true,
	})

	res, body := f.registerWith("player", "player@example.com", "correct-horse-battery",
		[]map[string]any{
			{"field_id": publicField, "value": "MIT"},
			{"field_id": privateField, "value": "top-secret"},
		})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("register with answers: %d (%s)", res.StatusCode, body)
	}
	playerID := f.userID("player@example.com")

	res, body = f.do(http.MethodGet, "/api/v1/users/"+itoa(playerID), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("public profile: %d (%s)", res.StatusCode, body)
	}
	var pub publicProfileView
	if err := json.Unmarshal(body, &pub); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}

	if len(pub.Fields) != 1 {
		t.Fatalf("public fields = %d, want exactly 1 (only the public one): %s", len(pub.Fields), body)
	}
	got := pub.Fields[0]
	if got.ID != publicField || got.Name != "School" || string(got.Value) != `"MIT"` {
		t.Fatalf("public field = %+v, want the School field valued \"MIT\"", got)
	}
	for _, fl := range pub.Fields {
		if fl.ID == privateField || fl.Name == "Badge" || string(fl.Value) == `"top-secret"` {
			t.Fatal("public profile leaked a private custom field")
		}
	}

	// Belt and braces on the raw bytes: the private answer must not appear anywhere in the document.
	if doc := string(body); strings.Contains(doc, "top-secret") || strings.Contains(doc, "Badge") {
		t.Fatalf("private field content present in the public profile body: %s", body)
	}
}
