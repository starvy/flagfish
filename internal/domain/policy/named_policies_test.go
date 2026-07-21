package policy_test

import (
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// ---------------------------------------------------------------------------
// Fixtures. A running, public, teams-mode CTF with a verified player on a team.
// Every test below perturbs exactly one thing.
// ---------------------------------------------------------------------------

func runningEvent() policy.Event {
	return policy.Event{
		Mode:            account.ModeTeams,
		SetupDone:       true,
		ChallengeVis:    policy.VisPublic,
		ScoreVis:        policy.VisPublic,
		AccountVis:      policy.VisPublic,
		RegistrationVis: policy.VisPublic,
		Phase:           policy.PhaseRunning,
		TeamCreation:    true,
	}
}

func player() policy.Principal {
	return policy.Principal{
		Authed: true, Verified: true,
		ProfileComplete: true, TeamProfileComplete: true,
		UserID: 10, AccountID: 3,
	}
}

func admin() policy.Principal {
	p := player()
	p.IsAdmin = true
	return p
}

func anon() policy.Principal { return policy.Principal{} }

// ---------------------------------------------------------------------------
// The thirteen. One test per NamedPolicy. If you are here because one of these
// failed, read the NamedPolicy entry before you "fix" the code.
// ---------------------------------------------------------------------------

func TestUnverifiedMoreRestrictedThanAnonymous(t *testing.T) {
	e := runningEvent()
	e.Mode = account.ModeUsers
	e.VerifyEmails = true
	e.ChallengeVis = policy.VisPublic

	unverified := player()
	unverified.Verified = false

	anonOut := policy.Decide(policy.Policy{E: e, P: anon(), R: policy.Request{Class: policy.ClassChallengeList}})
	if !anonOut.Allow {
		t.Fatalf("anonymous visitor must see the public challenge list, got %+v", anonOut)
	}

	unverifiedOut := policy.Decide(policy.Policy{E: e, P: unverified, R: policy.Request{Class: policy.ClassChallengeList}})
	if unverifiedOut.Allow || unverifiedOut.Reason != policy.ReasonUnverified {
		t.Fatalf("an authed-but-unconfirmed user must be BLOCKED on the same path an anonymous one may read, got %+v", unverifiedOut)
	}
}

func TestPauseBlocksAdminAttempt(t *testing.T) {
	e := runningEvent()
	e.Paused = true

	for _, pr := range []struct {
		name string
		p    policy.Principal
	}{{"player", player()}, {"admin", admin()}} {
		out := policy.Decide(policy.Policy{E: e, P: pr.p, R: policy.Request{Class: policy.ClassChallengeAttempt}})
		if out.Allow || out.Reason != policy.ReasonPaused {
			t.Errorf("%s: a paused CTF must block the attempt for EVERYONE (no admin exemption), got %+v", pr.name, out)
		}
	}

	// ...except behind ?preview, which short-circuits before the pause check.
	out := policy.Decide(policy.Policy{E: e, P: admin(), R: policy.Request{Class: policy.ClassChallengeAttempt, Preview: true}})
	if !out.Allow {
		t.Errorf("admin ?preview must short-circuit the pause gate, got %+v", out)
	}

	// Preview is read straight off the query string, so it may lift the pause only for an
	// admin. For a player it would be a one-parameter bypass: solve while the field is locked
	// out, take the first blood, and move the decay.
	out = policy.Decide(policy.Policy{E: e, P: player(), R: policy.Request{Class: policy.ClassChallengeAttempt, Preview: true}})
	if out.Allow || out.Reason != policy.ReasonPaused {
		t.Errorf("player ?preview must NOT lift the pause gate, got %+v", out)
	}
}

// The clock stopping is the end of scoring, whatever view_after_ctf says. That setting is the
// ordinary post-event courtesy — leave the challenges readable — and it must not also leave them
// solvable, or the standings keep moving after everyone has gone home.
func TestEndedCTFBlocksScoringEvenWhenViewAfterCTF(t *testing.T) {
	e := runningEvent()
	e.Phase = policy.PhaseEnded
	e.ViewAfterCTF = true

	for _, c := range []policy.RouteClass{
		policy.ClassChallengeAttempt, policy.ClassHintUnlock, policy.ClassSolutionUnlock,
	} {
		out := policy.Decide(policy.Policy{E: e, P: player(), R: policy.Request{Class: c}})
		if out.Allow || out.Reason != policy.ReasonCTFEnded {
			t.Errorf("%v: view_after_ctf must not reopen scoring after the event ends, got %+v", c, out)
		}
	}

	// The read classes are exactly what view_after_ctf is for, and they stay open.
	out := policy.Decide(policy.Policy{E: e, P: player(), R: policy.Request{Class: policy.ClassChallengeList}})
	if !out.Allow {
		t.Errorf("view_after_ctf must keep the challenges readable after the end, got %+v", out)
	}
}

// Enrollment is the one window that opens before the clock does and shuts when it stops. The
// three phases are asserted separately because each one is a different product statement, and
// the tempting "just set timeGated" fix gets two of the three wrong.
func TestEnrollmentWindow(t *testing.T) {
	freeze := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	enrollment := []policy.RouteClass{policy.ClassTeamEnrollment, policy.ClassTeamCreate}

	teamless := player()
	teamless.Teamless = true

	// Teamless, because the create gate refuses anyone already on a team — admins included,
	// which is a separate rule and not the one under test here.
	teamlessAdmin := admin()
	teamlessAdmin.Teamless = true

	// Before the start: registration time. A player must be able to form and join a team, and
	// timeGated would 403 them here — the mistake this policy exists to name.
	e := runningEvent()
	e.Phase = policy.PhaseBeforeStart
	for _, c := range enrollment {
		out := policy.Decide(policy.Policy{E: e, P: teamless, R: policy.Request{Class: c}})
		if !out.Allow {
			t.Errorf("%s: enrollment must be OPEN before the CTF starts — that is when teams are formed. Got %+v", c, out)
		}
	}

	// During a freeze: the board is hidden, the game is not stopped. A team that gains a member
	// in the last hour is playing, not cheating.
	e = runningEvent()
	e.FreezeAt = &freeze
	for _, c := range enrollment {
		out := policy.Decide(policy.Policy{E: e, P: teamless, R: policy.Request{Class: c}})
		if !out.Allow {
			t.Errorf("%s: the freeze hides the scoreboard, it does not close enrollment. Got %+v", c, out)
		}
	}

	// After the end: shut. Both with and without view_after_ctf — that setting reopens reading,
	// never a roster.
	for _, viewAfter := range []bool{false, true} {
		e = runningEvent()
		e.Phase = policy.PhaseEnded
		e.ViewAfterCTF = viewAfter
		for _, c := range enrollment {
			out := policy.Decide(policy.Policy{E: e, P: teamless, R: policy.Request{Class: c}})
			if out.Allow || out.Status != 403 || out.Reason != policy.ReasonCTFEnded {
				t.Errorf("%s (view_after_ctf=%v): joining the winners after the clock stops must be refused. Got %+v",
					c, viewAfter, out)
			}
		}

		// The team page itself outlives the event, or a player cannot see the team they played on.
		out := policy.Decide(policy.Policy{E: e, P: player(), R: policy.Request{Class: policy.ClassTeamSelf}})
		if !out.Allow {
			t.Errorf("view_after_ctf=%v: reading your own team must survive the end of the event. Got %+v", viewAfter, out)
		}

		// The organiser's override, same shape as the one on the clock gate above.
		for _, c := range enrollment {
			if out := policy.Decide(policy.Policy{E: e, P: teamlessAdmin, R: policy.Request{Class: c}}); !out.Allow {
				t.Errorf("%s: an admin fixing a roster after the event is doing their job. Got %+v", c, out)
			}
		}
	}
}

func TestPausedHintUnlockStillCharges(t *testing.T) {
	e := runningEvent()
	e.Paused = true

	for _, class := range []policy.RouteClass{policy.ClassHintUnlock, policy.ClassSolutionUnlock} {
		out := policy.Decide(policy.Policy{E: e, P: player(), R: policy.Request{Class: class}})
		if !out.Allow {
			t.Errorf("%s: pause gates the ATTEMPT and nothing else. A player may still burn score on a hint "+
				"for a challenge they cannot attempt. This looks like a missing check; it is not. Got %+v", class, out)
		}
	}
}

func TestFreezeExemptionIsCallSiteDriven(t *testing.T) {
	freeze := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	e := runningEvent()
	e.FreezeAt = &freeze

	for _, tc := range []struct {
		name string
		p    policy.Principal
		r    policy.Request
		want bool // exempt from the freeze == sees live data
	}{
		// The headline: an admin on the public surface sees the frozen board.
		{"admin, public scoreboard", admin(), policy.Request{Class: policy.ClassScoreboard, Surface: policy.SurfacePublic}, false},
		// ?preview lets an admin peek at the live public board during a freeze; a player's does not.
		{"admin, public scoreboard, ?preview", admin(), policy.Request{Class: policy.ClassScoreboard, Surface: policy.SurfacePublic, Preview: true}, true},
		{"player, public scoreboard, ?preview", player(), policy.Request{Class: policy.ClassScoreboard, Surface: policy.SurfacePublic, Preview: true}, false},
		{"admin, admin scoreboard", admin(), policy.Request{Class: policy.ClassAdminScoreboard, Surface: policy.SurfaceAdmin}, true},
		{"admin, scoreboard detail on the public surface", admin(), policy.Request{Class: policy.ClassScoreboardDetail, Surface: policy.SurfacePublic}, false},
		// Challenge detail: frozen even for admins, unconditionally.
		{"admin, challenge detail", admin(), policy.Request{Class: policy.ClassChallengeDetail, Surface: policy.SurfaceAdmin}, false},
		// Challenge list: ?view=admin narrows an admin's exemption; it never grants one. A
		// player's ?view=admin must not be honoured, or diffing the frozen list against the
		// "admin" one reads off every solve that landed during the freeze.
		{"admin, challenge list, no view=admin", admin(), policy.Request{Class: policy.ClassChallengeList}, false},
		{"admin, challenge list, view=admin", admin(), policy.Request{Class: policy.ClassChallengeList, AdminView: true}, true},
		{"player, challenge list, view=admin", player(), policy.Request{Class: policy.ClassChallengeList, AdminView: true}, false},
		// The one genuinely role-driven site, with an inverted ?preview default.
		{"admin, per-challenge solves", admin(), policy.Request{Class: policy.ClassChallengeSolves}, true},
		{"admin, per-challenge solves, ?preview", admin(), policy.Request{Class: policy.ClassChallengeSolves, Preview: true}, false},
		{"player, per-challenge solves", player(), policy.Request{Class: policy.ClassChallengeSolves}, false},
		{"statistics", admin(), policy.Request{Class: policy.ClassStatistics}, true},
		{"export", admin(), policy.Request{Class: policy.ClassExport}, true},
	} {
		p := policy.Policy{E: e, P: tc.p, R: tc.r}
		if got := policy.FreezeExempt(p); got != tc.want {
			t.Errorf("%s: FreezeExempt = %v, want %v", tc.name, got, tc.want)
		}
		if got := policy.Frozen(p); got != !tc.want {
			t.Errorf("%s: Frozen = %v, want %v", tc.name, got, !tc.want)
		}
	}

	// With no freeze configured, nothing is frozen and the exemption is moot.
	e.FreezeAt = nil
	if policy.Frozen(policy.Policy{E: e, P: player(), R: policy.Request{Class: policy.ClassScoreboard}}) {
		t.Error("no freeze configured, yet Frozen() is true")
	}
}

func TestAdminsOnlyVisibilityStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		class      policy.RouteClass
		setVis     func(*policy.Event)
		p          policy.Principal
		wantStatus int
		wantReason policy.Reason
	}{
		{
			"challenge/admins/authed -> 403", policy.ClassChallengeList,
			func(e *policy.Event) { e.ChallengeVis = policy.VisAdmins }, player(), 403, policy.ReasonAdminsOnly,
		},
		{
			"challenge/admins/anon -> login", policy.ClassChallengeList,
			func(e *policy.Event) { e.ChallengeVis = policy.VisAdmins }, anon(), 403, policy.ReasonAuthRequired,
		},
		// Accounts and scores hide their existence. Different disclosure, on purpose.
		{
			"account/admins/authed -> 404", policy.ClassAccountList,
			func(e *policy.Event) { e.AccountVis = policy.VisAdmins }, player(), 404, policy.ReasonNotFound,
		},
		{
			"score/admins/authed -> 404", policy.ClassScoreboard,
			func(e *policy.Event) { e.ScoreVis = policy.VisAdmins }, player(), 404, policy.ReasonNotFound,
		},
		{
			"score/hidden/authed -> 403", policy.ClassScoreboard,
			func(e *policy.Event) { e.ScoreVis = policy.VisHidden }, player(), 403, policy.ReasonScoresHidden,
		},
		{
			"score/hidden/admin -> allowed", policy.ClassScoreboard,
			func(e *policy.Event) { e.ScoreVis = policy.VisHidden }, admin(), 0, policy.ReasonNone,
		},
	} {
		e := runningEvent()
		tc.setVis(&e)
		out := policy.Decide(policy.Policy{E: e, P: tc.p, R: policy.Request{Class: tc.class}})

		if tc.wantStatus == 0 {
			if !out.Allow {
				t.Errorf("%s: want allow, got %+v", tc.name, out)
			}
			continue
		}
		if out.Allow || out.Status != tc.wantStatus || out.Reason != tc.wantReason {
			t.Errorf("%s: got %+v, want status %d reason %s", tc.name, out, tc.wantStatus, tc.wantReason)
		}
	}
}

// The policy is encoded as an absence, so the test asserts the absence.
func TestHiddenPrincipalIsNotAThing(t *testing.T) {
	// A hidden account is not distinguishable from any other account at L1: there
	// is no field to distinguish it with. It authenticates, plays and solves.
	// If this test stops compiling because someone added Principal.Hidden, the
	// policy has been broken and the scoreboard predicates are no longer the only
	// place hiddenness lives.
	out := policy.Decide(policy.Policy{E: runningEvent(), P: player(), R: policy.Request{Class: policy.ClassChallengeAttempt}})
	if !out.Allow {
		t.Fatalf("a hidden account must be able to solve: %+v", out)
	}

	for _, np := range policy.NamedPolicies {
		if np.Name == "PolicyHiddenIsNotBlocked" {
			return
		}
	}
	t.Fatal("PolicyHiddenIsNotBlocked vanished from the register")
}

func TestCapsIgnoreHiddenAndBannedAndAdminsBypass(t *testing.T) {
	for _, tc := range []struct {
		banned, hidden, counts bool
	}{
		{false, false, true},
		{true, false, false},
		{false, true, false}, // the hidden setup admin never consumes a slot
		{true, true, false},
	} {
		if got := policy.CountsTowardCap(tc.banned, tc.hidden); got != tc.counts {
			t.Errorf("CountsTowardCap(banned=%v, hidden=%v) = %v, want %v", tc.banned, tc.hidden, got, tc.counts)
		}
	}
	if !policy.BypassesCaps(admin()) {
		t.Error("admin creation paths bypass num_users/num_teams/team_size entirely")
	}
	if policy.BypassesCaps(player()) {
		t.Error("a player must not bypass the caps")
	}
}

func TestOwnScoreLiveUnderFreezeAndHiddenScores(t *testing.T) {
	e := runningEvent()
	e.ScoreVis = policy.VisHidden
	freeze := time.Now()
	e.FreezeAt = &freeze

	r := policy.NewRedactor(policy.Policy{E: e, P: player(), R: policy.Request{Class: policy.ClassAccountSelf}})
	if r.ScoresVisible {
		t.Fatal("score_visibility=hidden means scores are not visible, even to build the redactor")
	}

	score, place := 1337, 4
	f := policy.AccountFields{Score: &score, Place: &place}
	r.Self(&f)

	if f.Score == nil || *f.Score != 1337 {
		t.Error("a user's OWN score is always live: they can sum their own solves anyway")
	}
	if f.Place != nil {
		t.Error("a user's own PLACE is still gated: they cannot derive their rank, so it stays hidden")
	}

	// On any other account's view, both go.
	other := policy.AccountFields{Score: &score, Place: &place}
	r.Account(&other)
	if other.Score != nil || other.Place != nil {
		t.Error("another account's score and place must both be nulled (null, not 0)")
	}
}

func TestRegistrationMLC404sTheFormRoute(t *testing.T) {
	e := runningEvent()
	e.RegistrationVis = policy.VisMLC

	out := policy.Decide(policy.Policy{E: e, P: anon(), R: policy.Request{Class: policy.ClassRegister}})
	if out.Allow || out.Status != 404 {
		t.Fatalf("registration_visibility=mlc must 404 the form route, not crash, got %+v", out)
	}

	// `private` does the same: there is nobody to authenticate on a registration form.
	e.RegistrationVis = policy.VisPrivate
	if out := policy.Decide(policy.Policy{E: e, P: anon(), R: policy.Request{Class: policy.ClassRegister}}); out.Status != 404 {
		t.Fatalf("registration_visibility=private must 404, got %+v", out)
	}
}

// The authz bypass: no code path lets a banned principal through, whatever the
// credential. Fixed by construction.
func TestBannedPrincipalIsRejectedRegardlessOfCredential(t *testing.T) {
	e := runningEvent()

	banned := player()
	banned.Banned = true

	teamBanned := player()
	teamBanned.TeamBanned = true

	// The Policy struct carries no notion of which credential authenticated the
	// caller — cookie or API token — and that is the fix. There is one ban check and
	// one principal, so a token cannot route around a wall that a cookie hits.
	for _, class := range []policy.RouteClass{
		policy.ClassChallengeAttempt, policy.ClassChallengeList, policy.ClassScoreboard,
		policy.ClassTokens, policy.ClassSSE, policy.ClassAccountSelf, policy.ClassAdmin,
	} {
		for _, pr := range []struct {
			name string
			p    policy.Principal
			want policy.Reason
		}{{"banned user", banned, policy.ReasonBanned}, {"banned team", teamBanned, policy.ReasonTeamBanned}} {
			out := policy.Decide(policy.Policy{E: e, P: pr.p, R: policy.Request{Class: class}})
			if out.Allow || out.Status != 403 || out.Reason != pr.want {
				t.Errorf("%s on %s: got %+v, want 403 %s — a banned account must not reach ANY route, "+
					"and above all not flag submission", pr.name, class, out, pr.want)
			}
		}
	}

	// A banned admin is still banned.
	bannedAdmin := admin()
	bannedAdmin.Banned = true
	if out := policy.Decide(policy.Policy{E: e, P: bannedAdmin, R: policy.Request{Class: policy.ClassAdmin}}); out.Allow {
		t.Error("a banned admin is banned")
	}

	// Theme assets stay reachable, or the 403 page cannot render itself.
	if out := policy.Decide(policy.Policy{E: e, P: banned, R: policy.Request{Class: policy.ClassThemeAsset}}); !out.Allow {
		t.Error("theme assets are exempt from the ban wall, or the ban page has no CSS")
	}
}

func TestManualGradeLandsPreFreeze(t *testing.T) {
	freeze := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	submitted := freeze.Add(-2 * time.Hour) // the player submitted before the freeze

	solveDate := policy.SolveDateForManualGrade(submitted)

	if !solveDate.Equal(submitted) {
		t.Fatalf("a manually-graded solve is stamped with the ORIGINAL submission time, got %v", solveDate)
	}
	if !solveDate.Before(freeze) {
		t.Fatal("the backdated solve must land behind the freeze cutoff — that is the whole consequence")
	}
}

// The register itself must stay complete and honest.
func TestNamedPoliciesRegister(t *testing.T) {
	if len(policy.NamedPolicies) != 13 {
		t.Fatalf("the register holds %d policies, want 13. "+
			"Adding or removing one is a product change.", len(policy.NamedPolicies))
	}
	seen := map[string]bool{}
	for _, np := range policy.NamedPolicies {
		if np.Name == "" || np.Why == "" || np.Preserved == "" || np.Test == "" {
			t.Errorf("%+v: an entry with an empty field is a comment, not a policy", np)
		}
		if np.Kind != policy.Deliberate && np.Kind != policy.Corrected {
			t.Errorf("%s: kind %d is neither Deliberate nor Corrected", np.Name, np.Kind)
		}
		if seen[np.Name] {
			t.Errorf("%s: duplicate", np.Name)
		}
		seen[np.Name] = true
	}
}
