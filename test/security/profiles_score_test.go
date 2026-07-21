//go:build integration

package security

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The public profiles — GET /users/{id} and GET /teams/{id} — are scoreboard rows with a history
// attached, so score_visibility has to govern their figures exactly as it governs the board. It does
// it by redaction, not by a route gate: account_visibility decides whether the page exists at all, so
// a score-hide must withhold the score and the solve history while leaving the page itself standing.
// A hidden score is null on the wire — never absent, never 0 — and a withheld solve list is empty,
// because the length of that list is a solve count.

func userProfilePath(id int64) string  { return "/api/v1/users/" + strconv.FormatInt(id, 10) }
func teamProfilePath(id int64) string  { return "/api/v1/teams/" + strconv.FormatInt(id, 10) }
func scoreHistoryPath(id int64) string { return "/api/v1/scoreboard/" + strconv.FormatInt(id, 10) }

type scorePointWire struct {
	Date  time.Time `json:"date"`
	Delta int64     `json:"delta"`
	Score int64     `json:"score"`
}

type userProfileWire struct {
	Name   string `json:"name"`
	Score  *int64 `json:"score"`
	Solves []struct {
		ChallengeName string `json:"challenge_name"`
		Value         int32  `json:"value"`
	} `json:"solves"`
	Points []scorePointWire `json:"points"`
}

// lastPoint is the final cumulative score on a curve, or 0 when the curve is empty.
func lastPoint(pts []scorePointWire) int64 {
	if len(pts) == 0 {
		return 0
	}
	return pts[len(pts)-1].Score
}

// solveAtTeam stamps a solve with both the user and the team, the shape a teams-mode solve carries.
func (f *fixture) solveAtTeam(challengeID, userID, teamID int64, value int, at time.Time) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO solves (challenge_id, user_id, team_id, value, date) VALUES ($1,$2,$3,$4,$5)`,
		challengeID, userID, teamID, value, at); err != nil {
		f.t.Fatalf("seed team solve: %v", err)
	}
}

func decodeUserProfile(t *testing.T, body string) userProfileWire {
	t.Helper()
	var out userProfileWire
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode user profile: %v (%s)", err, body)
	}
	return out
}

// TestS50 — a user's public profile withholds the score and the solve history when scores are
// hidden, without turning that into a 404. account_visibility, a different setting, is what hides the
// page's existence — and still does.
func TestS50_UserProfileRedactsScoreAndSolves(t *testing.T) {
	cases := []struct {
		name        string
		cfg         []func(*fixOpts)
		asAdmin     bool
		wantStatus  int
		wantResolve bool // the page renders (name present)
		wantScore   *int64
		wantSolves  int
	}{
		{
			name:        "public: a player sees the score and the solves",
			wantStatus:  http.StatusOK,
			wantResolve: true,
			wantScore:   i64(300),
			wantSolves:  1,
		},
		{
			// The redactor, not the gate: the account is public, so the page stands — but the figure
			// and the history are withheld. A null score is not a 404.
			name:        "score_visibility=admins: the page stands, the figures do not",
			cfg:         []func(*fixOpts){withConfig("score_visibility", "admins")},
			wantStatus:  http.StatusOK,
			wantResolve: true,
			wantScore:   nil,
			wantSolves:  0,
		},
		{
			name:        "score_visibility=admins: an admin sees everything",
			cfg:         []func(*fixOpts){withConfig("score_visibility", "admins")},
			asAdmin:     true,
			wantStatus:  http.StatusOK,
			wantResolve: true,
			wantScore:   i64(300),
			wantSolves:  1,
		},
		{
			// The gate, not the redactor: hiding accounts hides existence — a 404, unchanged by this
			// work. This is the line that proves a score-hide is not an existence-hide.
			name:       "account_visibility=admins: the page does not exist for a player",
			cfg:        []func(*fixOpts){withConfig("account_visibility", "admins")},
			wantStatus: http.StatusNotFound,
		},
		{
			name:        "account_visibility=admins: an admin still resolves it",
			cfg:         []func(*fixOpts){withConfig("account_visibility", "admins")},
			asAdmin:     true,
			wantStatus:  http.StatusOK,
			wantResolve: true,
			wantScore:   i64(300),
			wantSolves:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, tc.cfg...)
			ada := f.user("Ada", pw)
			ch := f.challenge("Sanity", 300)
			f.solveAt(ch, ada, 300, time.Now().Add(-time.Hour))

			res := f.do(http.MethodGet, userProfilePath(ada), withCookie(f.session(t, tc.asAdmin)))
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", res.StatusCode, tc.wantStatus, res.Body)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}

			prof := decodeUserProfile(t, res.Body)
			if tc.wantResolve && prof.Name != "Ada" {
				t.Errorf("name = %q, want Ada — the account must still resolve", prof.Name)
			}
			if !sameScore(prof.Score, tc.wantScore) {
				t.Errorf("score = %s, want %s", showScore(prof.Score), showScore(tc.wantScore))
			}
			if len(prof.Solves) != tc.wantSolves {
				t.Errorf("solves = %d, want %d", len(prof.Solves), tc.wantSolves)
			}
			// The score-over-time curve rides the same gate: shown when the figure is, withheld with
			// it. A withheld curve is an empty series, and a shown one ends at the shown score.
			if tc.wantScore == nil {
				if len(prof.Points) != 0 {
					t.Errorf("score curve = %d points, want none — a hidden score hides its curve", len(prof.Points))
				}
			} else if got := lastPoint(prof.Points); got != *tc.wantScore {
				t.Errorf("score curve final = %d, want %d — the chart must end at the shown score", got, *tc.wantScore)
			}
			// Belt and braces: when the score is withheld the solved challenge's name must not reach
			// the body through any field.
			if tc.wantScore == nil && strings.Contains(res.Body, "Sanity") {
				t.Errorf("withheld solve history leaked the challenge name: %s", res.Body)
			}
		})
	}
}

// TestS51 — the profile's solve history stays clamped to the freeze, and the redaction work must not
// have cost that. The freeze exemption on this route is a property of the surface, not the role: the
// public profile is frozen for everyone, so a non-admin AND an admin both see only the solves that
// had landed by the freeze. An admin who wants the live history goes to the admin surface, where the
// URL says so — granting the exemption to the admin role here would let them diff the public page
// against the live one and read off every solve the freeze exists to hide.
func TestS51_UserProfileSolvesHonourTheFreeze(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := setup(t, withConfig("freeze", strconv.FormatInt(freeze.Unix(), 10)))

	ada := f.user("Ada", pw)
	early := f.challenge("Early", 100)
	late := f.challenge("Late", 100)
	f.solveAt(early, ada, 100, freeze.Add(-time.Hour))
	f.solveAt(late, ada, 100, time.Now())

	for _, admin := range []bool{false, true} {
		prof := decodeUserProfile(t, f.do(http.MethodGet, userProfilePath(ada), withCookie(f.session(t, admin))).Body)
		if names := solveNames(prof); !equalNames(names, []string{"Early"}) {
			t.Errorf("admin=%v sees %v, want only Early — the public profile is surface-frozen, not role-exempt", admin, names)
		}
	}
}

// TestS52 — the solve history is bounded. A heavy solver's profile is a public document, and one
// request must not be able to pull an unbounded list. The headline score is summed independently, so
// the cap trims the list without ever touching the total.
func TestS52_UserProfileSolvesAreBounded(t *testing.T) {
	const (
		seeded  = 101 // one past the cap
		maxRows = 100
		value   = 7
	)

	f := setup(t)
	ada := f.user("Ada", pw)
	f.seedUserSolves(ada, seeded, value)

	prof := decodeUserProfile(t, f.do(http.MethodGet, userProfilePath(ada), withCookie(f.session(t, false))).Body)
	if len(prof.Solves) != maxRows {
		t.Errorf("solve history = %d rows, want the cap of %d", len(prof.Solves), maxRows)
	}
	// The score is the whole ledger, not the capped page: trimming the list must not shrink the total.
	if want := int64(seeded * value); prof.Score == nil || *prof.Score != want {
		t.Errorf("score = %s, want %d — the total sums every solve, not just the shown page", showScore(prof.Score), want)
	}
}

type teamProfileWire struct {
	Name    string `json:"name"`
	Score   *int64 `json:"score"`
	Members []struct {
		Name       string `json:"name"`
		Points     *int64 `json:"points"`
		SolveCount *int64 `json:"solve_count"`
	} `json:"members"`
}

func decodeTeamProfile(t *testing.T, body string) teamProfileWire {
	t.Helper()
	var out teamProfileWire
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode team profile: %v (%s)", err, body)
	}
	return out
}

// TestS53 — a team's public profile is the same shape of fix: score_visibility withholds the team
// total and every member's contribution, while the roster — who is on the team, which is account
// data — stays. Withholding the members entirely would turn a score-hide into a roster-hide.
func TestS53_TeamProfileRedactsScoresNotRoster(t *testing.T) {
	cases := []struct {
		name           string
		cfg            []func(*fixOpts)
		asAdmin        bool
		wantStatus     int
		wantScoreShown bool
	}{
		{
			name:           "public: the total and the per-member breakdown are shown",
			wantStatus:     http.StatusOK,
			wantScoreShown: true,
		},
		{
			name:           "score_visibility=admins: the roster stands, the figures are withheld",
			cfg:            []func(*fixOpts){withConfig("score_visibility", "admins")},
			wantStatus:     http.StatusOK,
			wantScoreShown: false,
		},
		{
			name:           "score_visibility=admins: an admin sees the figures",
			cfg:            []func(*fixOpts){withConfig("score_visibility", "admins")},
			asAdmin:        true,
			wantStatus:     http.StatusOK,
			wantScoreShown: true,
		},
		{
			name:       "account_visibility=admins: the team page does not exist for a player",
			cfg:        []func(*fixOpts){withConfig("account_visibility", "admins")},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, append([]func(*fixOpts){withTeamsMode()}, tc.cfg...)...)
			teamID := f.team("Alpha")
			captain := f.user("cap", pw)
			mate := f.user("mate", pw)
			f.assign(captain, teamID)
			f.assign(mate, teamID)
			f.score(teamID, captain, 500)

			res := f.do(http.MethodGet, teamProfilePath(teamID), withCookie(f.session(t, tc.asAdmin)))
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", res.StatusCode, tc.wantStatus, res.Body)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}

			team := decodeTeamProfile(t, res.Body)
			if team.Name != "Alpha" {
				t.Errorf("name = %q, want Alpha — the team must still resolve", team.Name)
			}
			// The roster is account data: both members are present whether or not scores are shown.
			if len(team.Members) != 2 {
				t.Fatalf("members = %d, want 2 — the roster is not score data and must not be withheld", len(team.Members))
			}

			if tc.wantScoreShown {
				if team.Score == nil || *team.Score != 500 {
					t.Errorf("team score = %s, want 500", showScore(team.Score))
				}
				for _, m := range team.Members {
					if m.Points == nil || m.SolveCount == nil {
						t.Errorf("member %q contribution withheld while scores are visible", m.Name)
					}
				}
			} else {
				if team.Score != nil {
					t.Errorf("team score = %s, want null — scores are hidden", showScore(team.Score))
				}
				for _, m := range team.Members {
					if m.Points != nil || m.SolveCount != nil {
						t.Errorf("member %q leaked a contribution while scores are hidden: points=%s count=%s",
							m.Name, showScore(m.Points), showScore(m.SolveCount))
					}
				}
				// Belt and braces: the 500-point figure must not reach the body through any field.
				if strings.Contains(res.Body, "500") {
					t.Errorf("a withheld score figure leaked in the body: %s", res.Body)
				}
			}
		})
	}
}

// TestS54 — the score-over-time endpoint that feeds the profile chart already gates on
// score_visibility at the route (it is the scoreboard family), so it hides existence outright rather
// than redacting: a non-admin gets a 404 when scores are hidden, an admin gets the curve. This pins
// that it stays that way — the profile fix leaves it untouched, and it must not regress open.
func TestS54_ScoreHistoryGatesOnScoreVisibility(t *testing.T) {
	t.Run("public: a player may read the curve", func(t *testing.T) {
		f := setup(t)
		ada := f.user("Ada", pw)
		if res := f.do(http.MethodGet, scoreHistoryPath(ada), withCookie(f.session(t, false))); res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", res.StatusCode, res.Body)
		}
	})

	t.Run("score_visibility=admins: a player gets a 404, an admin the curve", func(t *testing.T) {
		f := setup(t, withConfig("score_visibility", "admins"))
		ada := f.user("Ada", pw)
		if res := f.do(http.MethodGet, scoreHistoryPath(ada), withCookie(f.session(t, false))); res.StatusCode != http.StatusNotFound {
			t.Errorf("player status = %d, want 404 — the score chart hides its existence", res.StatusCode)
		}
		if res := f.do(http.MethodGet, scoreHistoryPath(ada), withCookie(f.session(t, true))); res.StatusCode != http.StatusOK {
			t.Errorf("admin status = %d, want 200", res.StatusCode)
		}
	})
}

type historyWire struct {
	Points []scorePointWire `json:"points"`
}

func decodeHistoryWire(t *testing.T, body string) historyWire {
	t.Helper()
	var out historyWire
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode score history: %v (%s)", err, body)
	}
	return out
}

// TestS55 — in teams mode a user's public chart must plot THAT user, never a team that merely shares
// their id. User ids and team ids are independent bigserials, so a user id collides with an unrelated
// team's id, and resolving the profile's curve through the scoreboard-detail endpoint (which keys on
// the team in teams mode) plots a stranger. The profile carries the user's own ledger instead — keyed
// on user_id — so the curve is the player's personal contribution, agreeing with the headline score.
//
// Remove that and the chart falls back to an empty series here: the assertion on the user's own curve
// fails, which is the regression this test exists to catch.
func TestS55_UserProfileChartIsOwnSeriesNotForeignTeam(t *testing.T) {
	f := setup(t, withTeamsMode())

	// The foreign team is seeded first so the teams sequence hands it the same id the first user will
	// get. This reproduces the collision exactly: a stranger's team whose id equals the user's.
	foreign := f.team("Strangers")
	own := f.team("Aces")
	ada := f.user("Ada", pw)
	if ada != foreign {
		t.Fatalf("test needs the user id to collide with the foreign team id: user=%d team=%d", ada, foreign)
	}
	f.assign(ada, own)

	// The foreign team's curve: a large, unmistakable total earned by someone who is not Ada.
	mate := f.user("Mate", pw)
	f.assign(mate, foreign)
	chForeign := f.challenge("Foreign", 900)
	f.solveAtTeam(chForeign, mate, foreign, 900, time.Now().Add(-2*time.Hour))

	// Ada's own curve: a small, distinct total stamped to Ada and her real team.
	chMine := f.challenge("Mine", 100)
	f.solveAtTeam(chMine, ada, own, 100, time.Now().Add(-time.Hour))

	sid := f.session(t, false)
	res := f.do(http.MethodGet, userProfilePath(ada), withCookie(sid))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("profile status = %d, want 200: %s", res.StatusCode, res.Body)
	}
	prof := decodeUserProfile(t, res.Body)

	// The chart under Ada's profile is Ada's own contribution: it ends at her 100, not the stranger
	// team's 900. The headline score is summed from the same stamped ledger, so the two agree.
	if got := lastPoint(prof.Points); got != 100 {
		t.Fatalf("profile chart final = %d, want 100 (Ada's own); 900 would be the foreign team's curve", got)
	}
	if prof.Score == nil || *prof.Score != 100 {
		t.Errorf("headline score = %s, want 100 — the chart and the headline must agree", showScore(prof.Score))
	}
	// The foreign total must not surface anywhere in Ada's profile document.
	if strings.Contains(res.Body, "900") {
		t.Errorf("a foreign team's figure leaked into the profile body: %s", res.Body)
	}

	// The smoking gun: the endpoint the chart used to be fetched from resolves Ada's id as a TEAM id
	// in teams mode, handing back the stranger's 900. This is why the profile must not use it.
	hist := decodeHistoryWire(t, f.do(http.MethodGet, scoreHistoryPath(ada), withCookie(sid)).Body)
	if got := lastPoint(hist.Points); got != 900 {
		t.Fatalf("scoreboard-detail for the user id returned %d, want the colliding team's 900 — the collision assumption is stale", got)
	}
}

// TestS56 — users mode is unregressed: the user IS the scoring account, so the profile chart is the
// user's own curve and matches the scoreboard-detail endpoint for the same id. The fix keys the
// profile curve on user_id, which in users mode is exactly what the scoreboard resolves too.
func TestS56_UserProfileChartUsersModeUnchanged(t *testing.T) {
	f := setup(t)
	ada := f.user("Ada", pw)
	ch := f.challenge("Sanity", 300)
	f.solveAt(ch, ada, 300, time.Now().Add(-time.Hour))

	sid := f.session(t, false)
	prof := decodeUserProfile(t, f.do(http.MethodGet, userProfilePath(ada), withCookie(sid)).Body)
	if got := lastPoint(prof.Points); got != 300 {
		t.Fatalf("profile chart final = %d, want 300 — the user's own curve", got)
	}
	// In users mode the two paths resolve the same account, so their curves agree.
	hist := decodeHistoryWire(t, f.do(http.MethodGet, scoreHistoryPath(ada), withCookie(sid)).Body)
	if got := lastPoint(hist.Points); got != 300 {
		t.Fatalf("scoreboard-detail final = %d, want 300 — users-mode resolution must not regress", got)
	}
}

// --- helpers ---

func i64(v int64) *int64 { return &v }

func sameScore(got, want *int64) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func showScore(p *int64) string {
	if p == nil {
		return "null"
	}
	return strconv.FormatInt(*p, 10)
}

func solveNames(p userProfileWire) []string {
	names := make([]string, 0, len(p.Solves))
	for _, s := range p.Solves {
		names = append(names, s.ChallengeName)
	}
	return names
}

// seedUserSolves gives a user n solves on n distinct visible challenges in one round trip, each
// stamped at a distinct time so the newest-first page is deterministic.
func (f *fixture) seedUserSolves(userID int64, n, value int) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
        WITH ch AS (
            INSERT INTO challenges (name, category, value, state)
            SELECT 'bulk-'||i, 'misc', $2, 'visible'
              FROM generate_series(1, $1) AS i
            RETURNING id
        )
        INSERT INTO solves (challenge_id, user_id, value, date)
        SELECT id, $3, $2, now() - (id * interval '1 second') FROM ch`,
		n, value, userID); err != nil {
		f.t.Fatalf("seed user solves: %v", err)
	}
}
