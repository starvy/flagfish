package policy

import "time"

// Kind separates the two reasons a behavior earns an entry here.
type Kind int

const (
	// Deliberate marks game semantics that look like bugs and are not. Changing one
	// changes the game.
	Deliberate Kind = iota + 1
	// Corrected marks a genuine defect that was fixed. It is recorded only because
	// the corrected behavior lives inside this layer, where it would otherwise be
	// mistaken for one of the deliberate quirks and "restored".
	Corrected
)

// NamedPolicy is one behavior of this layer that a well-meaning engineer will, at
// some point, open a PR to "fix".
//
// The rule: preserve the behavior, but make it an explicit, named, tested decision
// rather than an emergent consequence of how the code happens to be structured.
// Each entry names the test that fails if someone does open that PR — and the test
// is the comment that cannot rot, because a comment saying "this is deliberate" is
// indistinguishable from a comment somebody left on a bug.
//
// None of these may be changed without a product decision.
type NamedPolicy struct {
	Name string
	Kind Kind
	// Why records the behavior in one sentence.
	Why string
	// Preserved records the construction that makes it true — not the intention.
	Preserved string
	// Test is the test that fails if someone "fixes" it.
	Test string
}

// NamedPolicies is the register. A diff to this list is a product change and must
// be reviewed as one.
var NamedPolicies = []NamedPolicy{
	{
		Name: "PolicyUnverifiedStricterThanAnonymous", Kind: Deliberate,
		Why: "With verify_emails on and challenge_visibility=public, an ANONYMOUS visitor sees the " +
			"challenge list but an authed-but-unconfirmed user gets 403. Being logged out is strictly " +
			"more permissive than being logged in and unverified.",
		Preserved: "the `pr.Authed &&` conjunct in Decide's verification branch. Deleting it LOOSENS " +
			"the gate for logged-in users; it does not tighten it for anonymous ones.",
		Test: "TestUnverifiedMoreRestrictedThanAnonymous",
	},
	{
		Name: "PolicyPauseHasNoAdminExemption", Kind: Deliberate,
		Why:       "While the CTF is paused, NO ONE — not even an admin — can record a solve through the attempt endpoint.",
		Preserved: "the pause branch in Decide reads e.Paused and r.Preview only. It never reads pr.IsAdmin.",
		Test:      "TestPauseBlocksAdminAttempt",
	},
	{
		Name: "PolicyPauseDoesNotBlockUnlocks", Kind: Deliberate,
		Why: "Pause gates EXACTLY ONE mutation point: the attempt. A player can still burn score on " +
			"hints for a challenge they cannot currently attempt.",
		Preserved: "`r.Class == ClassChallengeAttempt` in the pause branch. It looks exactly like a missing check.",
		Test:      "TestPausedHintUnlockStillCharges",
	},
	{
		Name: "PolicyFreezeExemptionIsCallSiteDriven", Kind: Deliberate,
		Why: "'Admins always see live data' is FALSE. The freeze exemption is a property of the SURFACE, " +
			"not the caller: an admin loading the PUBLIC scoreboard sees the FROZEN board, and the " +
			"challenge-detail solve count is frozen for admins unconditionally.",
		Preserved: "FreezeExempt is the only function that computes it, and pr.IsAdmin appears in exactly " +
			"one of its arms — the one surface that is genuinely role-driven.",
		Test: "TestFreezeExemptionIsCallSiteDriven",
	},
	{
		Name: "PolicyAdminsOnlyHidesExistenceForAccountsNotChallenges", Kind: Deliberate,
		Why: "visibility=admins yields 403 on challenges but 404 on scores and accounts. Existence is " +
			"hidden for accounts and scores; challenges are merely gated.",
		Preserved: "the `kind == VisChallenge` branch in checkVis.",
		Test:      "TestAdminsOnlyVisibilityStatusCodes",
	},
	{
		Name: "PolicyHiddenIsNotBlocked", Kind: Deliberate,
		Why: "The ban wall blocks `banned`, never `hidden`. A hidden account authenticates, plays and " +
			"solves normally — it is merely absent from listings, standings and caps.",
		Preserved: "Principal has NO Hidden field. Hiddenness is an L3 SQL predicate (P1) and can never " +
			"reach an L1 decision. The absence of the field IS the policy.",
		Test: "TestHiddenPrincipalIsNotAThing",
	},
	{
		Name: "PolicyCapsExcludeMaskedAccountsAndAdminsBypass", Kind: Deliberate,
		Why: "num_users/num_teams count only non-banned, non-hidden accounts — so the hidden setup admin " +
			"never consumes a slot — and admin creation paths bypass the caps and team_size entirely.",
		Preserved: "CountsTowardCap and BypassesCaps below.",
		Test:      "TestCapsIgnoreHiddenAndBannedAndAdminsBypass",
	},
	{
		Name: "PolicyOwnScoreAlwaysVisible", Kind: Deliberate,
		Why: "/users/me returns a LIVE score — freeze-ignoring and visibility-ignoring — while `place` " +
			"stays gated. A user can sum their own solves anyway; they cannot derive their own rank.",
		Preserved: "Redactor.Self nulls Place and never Score.",
		Test:      "TestOwnScoreLiveUnderFreezeAndHiddenScores",
	},
	{
		Name: "PolicyZeroValueEventsAbsentFromStandings", Kind: Deliberate,
		Why: "An account whose only solves are zero-valued produces no standings row and is ABSENT from " +
			"the scoreboard — not shown at 0.",
		Preserved: "scoring.Event.Counts() + the INNER JOIN in the standings query.",
		Test:      "scoring.TestZeroOnlySolverAbsentFromBoard",
	},
	{
		Name: "PolicyManualGradeBackdatesSolve", Kind: Deliberate,
		Why: "PATCH /submissions/<id> type=correct creates the solve with date = the ORIGINAL submission " +
			"timestamp. A manually-graded solve therefore lands BEHIND the freeze cutoff if the submission " +
			"predates it, and moves the account's tiebreak key backwards. It is the only thing that can " +
			"move a row across the freeze boundary after the fact.",
		Preserved: "SolveDateForManualGrade below — a named function, so the choice is visible at the call site.",
		Test:      "TestManualGradeLandsPreFreeze",
	},
	{
		Name: "PolicyEnrollmentClosesAtTheEndOnly", Kind: Corrected,
		Why: "Team enrollment was gated on nothing at all, so a player could join the winners after " +
			"the clock stopped or shed a team to break an anti-cheat association. It is now open BEFORE " +
			"the start (that is registration), open during the freeze (the freeze hides the board, it " +
			"does not stop play) and closed once the event ends — and view_after_ctf does not reopen it.",
		Preserved: "the closesAtEnd attribute and its own gate in Decide. Setting timeGated on these " +
			"classes instead looks like the tidier fix and is wrong: it denies enrollment BEFORE the " +
			"start, which is when a team is actually assembled, and it makes view_after_ctf reopen a " +
			"roster. Reading /me/team is a separate class for the same reason — it must outlive the event.",
		Test: "TestEnrollmentWindow",
	},
	{
		Name: "PolicyRegistrationMLCTreatedAsPrivate", Kind: Corrected,
		Why: "registration_visibility='mlc' means the registration form route is unavailable and accounts " +
			"are created only via the MLC OAuth callback; a value with no explicit arm would otherwise " +
			"fall through and 500. We return 404, as `private` does.",
		Preserved: "the VisMLC arm of checkVis. Listed here because it sits inside the visibility gate and " +
			"would otherwise be mistaken for one of the deliberate quirks.",
		Test: "TestRegistrationMLC404sTheFormRoute",
	},
	{
		Name: "PolicyBanCoversTokenAuth", Kind: Corrected,
		Why: "A ban wall that runs only on the session path silently fails to cover API tokens: a banned " +
			"user holding a valid token would keep full API access INCLUDING FLAG SUBMISSION. That is an " +
			"authz bypass, not a quirk to preserve.",
		Preserved: "the Principal is resolved from EITHER credential before Decide runs, and Decide reads " +
			"pr.Banned regardless of which one it was. There is one ban check and one principal, so the bug " +
			"is not reachable by accident.",
		Test: "TestBannedPrincipalIsRejectedRegardlessOfCredential",
	},
}

// CountsTowardCap reports whether an account consumes one of the num_users /
// num_teams slots. Banned and hidden accounts do not — which is why the hidden
// setup admin never eats a slot (PolicyCapsExcludeMaskedAccountsAndAdminsBypass).
func CountsTowardCap(banned, hidden bool) bool { return !banned && !hidden }

// BypassesCaps reports whether this principal may exceed num_users / num_teams /
// team_size. Admin creation paths do, entirely.
func BypassesCaps(pr Principal) bool { return pr.IsAdmin }

// SolveDateForManualGrade returns the date to stamp on a solve created by an admin
// manually grading a submission: the original submission timestamp, not now
// (PolicyManualGradeBackdatesSolve).
//
// It exists as a named function precisely so that the call site cannot quietly
// pass time.Now() instead. That would look like a bug fix and would silently move
// solves across the freeze boundary.
func SolveDateForManualGrade(submittedAt time.Time) time.Time { return submittedAt }
