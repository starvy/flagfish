package policy_test

import (
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// The golden decision table. Every row is a product statement; a diff here is a
// product change and must be reviewed as one.
func TestDecideTable(t *testing.T) {
	teamless := player()
	teamless.Teamless = true

	unverified := player()
	unverified.Verified = false

	incomplete := player()
	incomplete.ProfileComplete = false

	mustChange := player()
	mustChange.ForcePasswordChange = true

	for _, tc := range []struct {
		name     string
		event    func(*policy.Event)
		p        policy.Principal
		r        policy.Request
		allow    bool
		status   int
		redirect string
		reason   policy.Reason
	}{
		// --- setup ------------------------------------------------------------
		{
			name: "setup incomplete redirects everything", event: func(e *policy.Event) { e.SetupDone = false },
			p: anon(), r: policy.Request{Class: policy.ClassIndex},
			redirect: "/setup", reason: policy.ReasonSetupIncomplete,
		},
		{
			name: "setup incomplete allows the setup route", event: func(e *policy.Event) { e.SetupDone = false },
			p: anon(), r: policy.Request{Class: policy.ClassSetup}, allow: true,
		},
		{
			name: "setup route stops existing once setup is done",
			p:    admin(), r: policy.Request{Class: policy.ClassSetup}, status: 404, reason: policy.ReasonNotFound,
		},

		// --- password change --------------------------------------------------
		{
			name: "forced password change redirects",
			p:    mustChange, r: policy.Request{Class: policy.ClassChallengeList},
			redirect: "/reset_password", reason: policy.ReasonPasswordChangeRequired,
		},
		{
			name: "forced password change exempts the reset route itself",
			p:    mustChange, r: policy.Request{Class: policy.ClassReset}, allow: true,
		},

		// --- mode -------------------------------------------------------------
		{
			name: "team routes 404 in users mode", event: func(e *policy.Event) { e.Mode = account.ModeUsers },
			p: player(), r: policy.Request{Class: policy.ClassTeamEnrollment},
			status: 404, reason: policy.ReasonNotFound,
		},

		// --- auth -------------------------------------------------------------
		{
			name: "anonymous attempt is 403, not a redirect",
			p:    anon(), r: policy.Request{Class: policy.ClassChallengeAttempt},
			status: 403, reason: policy.ReasonAuthenticationRequired,
		},
		{
			name: "anonymous /me redirects to login",
			p:    anon(), r: policy.Request{Class: policy.ClassAccountSelf},
			status: 403, redirect: "/login", reason: policy.ReasonAuthRequired,
		},
		{
			name: "private challenges require auth", event: func(e *policy.Event) { e.ChallengeVis = policy.VisPrivate },
			p: anon(), r: policy.Request{Class: policy.ClassChallengeList},
			status: 403, redirect: "/login", reason: policy.ReasonAuthRequired,
		},
		{name: "public challenges do not", p: anon(), r: policy.Request{Class: policy.ClassChallengeList}, allow: true},

		// --- admin surface ----------------------------------------------------
		{
			name: "admin surface rejects players", p: player(), r: policy.Request{Class: policy.ClassAdmin},
			status: 403, reason: policy.ReasonAdminRequired,
		},
		{name: "admin surface admits admins", p: admin(), r: policy.Request{Class: policy.ClassAdmin}, allow: true},

		// --- verification -----------------------------------------------------
		{
			name: "unverified is blocked when verify_emails is on", event: func(e *policy.Event) { e.VerifyEmails = true },
			p: unverified, r: policy.Request{Class: policy.ClassChallengeList},
			status: 403, redirect: "/confirm", reason: policy.ReasonUnverified,
		},
		{
			name: "unverified admin is not blocked", event: func(e *policy.Event) { e.VerifyEmails = true },
			p: func() policy.Principal { a := admin(); a.Verified = false; return a }(),
			r: policy.Request{Class: policy.ClassChallengeList}, allow: true,
		},
		{
			name: "unverified is fine when verify_emails is off",
			p:    unverified, r: policy.Request{Class: policy.ClassChallengeList}, allow: true,
		},

		// --- profile ----------------------------------------------------------
		{
			name: "incomplete profile redirects to settings",
			p:    incomplete, r: policy.Request{Class: policy.ClassChallengeList},
			redirect: "/settings", reason: policy.ReasonIncompleteProfile,
		},
		{
			name:   "incomplete TEAM profile is a 403, not a redirect",
			p:      func() policy.Principal { p := player(); p.TeamProfileComplete = false; return p }(),
			r:      policy.Request{Class: policy.ClassChallengeList},
			status: 403, reason: policy.ReasonIncompleteTeamProfile,
		},

		// --- teams ------------------------------------------------------------
		{
			name: "teamless cannot reach challenges in teams mode",
			p:    teamless, r: policy.Request{Class: policy.ClassChallengeList},
			status: 403, redirect: "/team", reason: policy.ReasonTeamRequired,
		},
		{
			name: "teamless CAN reach the enrollment page",
			p:    teamless, r: policy.Request{Class: policy.ClassTeamEnrollment}, allow: true,
		},
		{
			name:  "team creation disabled",
			event: func(e *policy.Event) { e.TeamCreation = false },
			p:     teamless, r: policy.Request{Class: policy.ClassTeamCreate},
			status: 403, reason: policy.ReasonTeamCreationDisabled,
		},
		{
			name: "already on a team cannot create another",
			p:    player(), r: policy.Request{Class: policy.ClassTeamCreate},
			status: 403, reason: policy.ReasonAlreadyOnTeam,
		},

		// --- phase ------------------------------------------------------------
		{
			name:  "before start, challenges are 403",
			event: func(e *policy.Event) { e.Phase = policy.PhaseBeforeStart },
			p:     player(), r: policy.Request{Class: policy.ClassChallengeList},
			status: 403, reason: policy.ReasonCTFNotStarted,
		},
		{
			name:  "before start, a TEAMLESS user is sent to enrollment instead",
			event: func(e *policy.Event) { e.Phase = policy.PhaseBeforeStart },
			p:     teamless, r: policy.Request{Class: policy.ClassChallengeList},
			status: 403, redirect: "/team", reason: policy.ReasonTeamRequired,
		},
		{
			name:  "after end, challenges are 403 unless view_after_ctf",
			event: func(e *policy.Event) { e.Phase = policy.PhaseEnded },
			p:     player(), r: policy.Request{Class: policy.ClassChallengeList},
			status: 403, reason: policy.ReasonCTFEnded,
		},
		{
			name:  "after end with view_after_ctf, challenges are readable",
			event: func(e *policy.Event) { e.Phase = policy.PhaseEnded; e.ViewAfterCTF = true },
			p:     player(), r: policy.Request{Class: policy.ClassChallengeList}, allow: true,
		},
		{
			name:  "admins ignore the clock",
			event: func(e *policy.Event) { e.Phase = policy.PhaseEnded },
			p:     admin(), r: policy.Request{Class: policy.ClassChallengeAttempt}, allow: true,
		},
		{
			name:  "the scoreboard is NOT time-gated",
			event: func(e *policy.Event) { e.Phase = policy.PhaseBeforeStart },
			p:     player(), r: policy.Request{Class: policy.ClassScoreboard}, allow: true,
		},

		// --- registration -----------------------------------------------------
		{
			name: "an authed user has no business registering",
			p:    player(), r: policy.Request{Class: policy.ClassRegister},
			redirect: "/challenges", reason: policy.ReasonAlreadyAuthed,
		},
	} {
		e := runningEvent()
		if tc.event != nil {
			tc.event(&e)
		}

		out := policy.Decide(policy.Policy{E: e, P: tc.p, R: tc.r})

		if out.Allow != tc.allow {
			t.Errorf("%s: allow = %v, want %v (%+v)", tc.name, out.Allow, tc.allow, out)
			continue
		}
		if tc.allow {
			continue
		}
		if out.Status != tc.status || out.Redirect != tc.redirect || out.Reason != tc.reason {
			t.Errorf("%s:\n got status=%d redirect=%q reason=%s\nwant status=%d redirect=%q reason=%s",
				tc.name, out.Status, out.Redirect, out.Reason, tc.status, tc.redirect, tc.reason)
		}
	}
}

// The ban wall and the setup gate run before everything else, on every class.
// This walks the whole route surface rather than a sample, because "we forgot one
// route" is exactly how an authz bypass is born.
func TestGlobalGatesCoverEveryRouteClass(t *testing.T) {
	banned := player()
	banned.Banned = true

	for _, class := range policy.AllClasses() {
		if class == policy.ClassSetup {
			// Setup is the one route that stops existing once setup is done, and a
			// route that does not exist 404s before anyone asks who is knocking.
			// That leaks nothing: 404 is strictly less informative than 403.
			continue
		}

		out := policy.Decide(policy.Policy{
			E: runningEvent(), P: banned, R: policy.Request{Class: class},
		})

		if class.ExemptFromBan() {
			if !out.Allow && out.Reason == policy.ReasonBanned {
				t.Errorf("%s is ban-exempt but was banned", class)
			}
			continue
		}
		if out.Reason != policy.ReasonBanned {
			t.Errorf("%s: a banned principal got %+v, want ReasonBanned. Every route is behind the wall.", class, out)
		}
	}
}

// An unauthenticated caller must never be able to trip the ban wall or the
// password-change redirect: both gate on pr.Authed, and an anonymous request has
// no account to be banned.
func TestAnonymousIsNeverBannedOrForcedToChangePassword(t *testing.T) {
	for _, class := range policy.AllClasses() {
		out := policy.Decide(policy.Policy{E: runningEvent(), P: anon(), R: policy.Request{Class: class}})
		switch out.Reason {
		case policy.ReasonBanned, policy.ReasonTeamBanned, policy.ReasonPasswordChangeRequired:
			t.Errorf("%s: anonymous caller got %s", class, out.Reason)
		default:
			// Every other reason is fine for an anonymous caller; this test only
			// pins the three that require an account to be reachable at all.
		}
	}
}

// Decide is pure: same input, same output, and it does not touch its argument.
func TestDecideIsPure(t *testing.T) {
	p := policy.Policy{E: runningEvent(), P: player(), R: policy.Request{Class: policy.ClassChallengeAttempt}}
	before := p

	first := policy.Decide(p)
	for range 100 {
		if got := policy.Decide(p); got != first {
			t.Fatalf("Decide is not deterministic: %+v vs %+v", got, first)
		}
	}
	if p != before {
		t.Fatal("Decide mutated its input")
	}
}

// Strict on both bounds. An instant exactly equal to a bound is inside the
// event — you are not late at exactly the deadline.
func TestPhaseAtBoundariesIsStrict(t *testing.T) {
	start := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name string
		now  time.Time
		want policy.Phase
	}{
		{"an hour early", start.Add(-time.Hour), policy.PhaseBeforeStart},
		{"exactly at start: not started yet", start, policy.PhaseBeforeStart},
		{"a nanosecond after start", start.Add(time.Nanosecond), policy.PhaseRunning},
		{"exactly at end: still running", end, policy.PhaseRunning},
		{"a nanosecond after end", end.Add(time.Nanosecond), policy.PhaseEnded},
	} {
		if got := policy.PhaseAt(tc.now, &start, &end); got != tc.want {
			t.Errorf("%s: PhaseAt = %s, want %s", tc.name, got, tc.want)
		}
	}

	// No window configured: the CTF is always running.
	if got := policy.PhaseAt(time.Now(), nil, nil); got != policy.PhaseRunning {
		t.Errorf("with no start/end the event is always running, got %s", got)
	}
	// End only.
	if got := policy.PhaseAt(start, nil, &end); got != policy.PhaseRunning {
		t.Errorf("with no start bound the event has always been running, got %s", got)
	}
}
