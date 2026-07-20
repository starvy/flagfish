package policy

import (
	"fmt"

	"github.com/starvy/flagfish/internal/domain/account"
)

// RouteClass is the policy-relevant identity of an endpoint. Every route in the
// product maps to exactly one, and the table below is the WHOLE L1 surface — if a
// route does not appear here, it cannot be gated, and that is a compile-time
// visible fact rather than a forgotten decorator.
type RouteClass uint8

const (
	ClassUnknown RouteClass = iota

	ClassThemeAsset // static assets. Exempt from the ban wall and the password-change redirect.
	ClassSetup
	ClassIndex
	ClassPages

	ClassRegister
	ClassLogin
	ClassLogout
	ClassReset
	ClassConfirm

	ClassChallengeList
	ClassChallengeDetail
	ClassChallengeAttempt
	ClassChallengeSolves

	ClassHintUnlock
	ClassSolutionUnlock

	ClassScoreboard
	ClassScoreboardDetail

	ClassAccountList
	ClassAccountDetail
	ClassAccountSelf
	// ClassPasswordChange is POST /me/password alone. It must be exempt from the forced-change
	// wall or a forced user loops forever: every route redirects them to change a password on
	// an endpoint the redirect itself blocks.
	ClassPasswordChange

	ClassTeamEnrollment
	ClassTeamCreate
	ClassTeamDetail

	ClassTokens
	ClassSSE
	ClassNotifications

	ClassAdmin
	ClassAdminScoreboard
	ClassStatistics
	ClassExport
)

// attrs is the route policy decision table, transcribed once.
//
// It is data, not code, precisely so that adding a route is a one-line diff that a
// reviewer can read as a product change — which is what it is.
type attrs struct {
	visGates []VisKind
	// modes: nil means "exists in both modes". Otherwise the route 404s outside
	// the listed modes.
	modes []account.Mode

	requiresAuth     bool
	requiresVerified bool
	requiresProfile  bool
	requiresTeam     bool
	timeGated        bool

	// mutatesScore: the route can move the standings — a solve, a first blood, a hint
	// deduction. view_after_ctf reopens the challenges for READING once the event is over;
	// it must never reopen scoring. Kept as a table attribute rather than a condition at the
	// gate so a new scoring route cannot forget to be excluded.
	mutatesScore bool

	adminOnly bool

	// exemptFromBan / exemptFromPasswordChange: the two global gates that run
	// before the table.
	exemptFromBan            bool
	exemptFromPasswordChange bool
}

var classAttrs = map[RouteClass]attrs{
	// ClassUnknown carries no attributes: an unclassified route gates on nothing
	// here and is handled explicitly by Decide. Spelled out rather than left to the
	// zero value so that adding a RouteClass is a compile-visible decision.
	ClassUnknown: {},

	ClassThemeAsset: {exemptFromBan: true, exemptFromPasswordChange: true},
	ClassSetup:      {exemptFromPasswordChange: true},
	ClassIndex:      {},
	ClassPages:      {},

	ClassRegister: {visGates: []VisKind{VisRegistration}},
	ClassLogin:    {exemptFromPasswordChange: true},
	ClassLogout:   {exemptFromPasswordChange: true},
	ClassReset:    {exemptFromPasswordChange: true},
	ClassConfirm:  {},

	// require_complete_profile is applied at exactly one call site upstream — the
	// challenges page — so a user with unfilled required fields is blocked from the
	// page but can still play through the API. That inconsistency is probably
	// unintended, so we gate all three challenge classes. If the product wants
	// otherwise, requiresProfile comes off these three rows and nothing else changes.
	ClassChallengeList:   {visGates: []VisKind{VisChallenge}, requiresVerified: true, requiresProfile: true, requiresTeam: true, timeGated: true},
	ClassChallengeDetail: {visGates: []VisKind{VisChallenge}, requiresVerified: true, requiresProfile: true, requiresTeam: true, timeGated: true},
	ClassChallengeAttempt: {
		visGates: []VisKind{VisChallenge}, requiresAuth: true, requiresVerified: true,
		requiresProfile: true, requiresTeam: true, timeGated: true, mutatesScore: true,
	},
	ClassChallengeSolves: {visGates: []VisKind{VisChallenge}, requiresVerified: true, timeGated: true},

	// No pause gate on the unlock classes. See PolicyPauseDoesNotBlockUnlocks.
	ClassHintUnlock:     {requiresAuth: true, requiresVerified: true, timeGated: true, mutatesScore: true},
	ClassSolutionUnlock: {requiresAuth: true, requiresVerified: true, timeGated: true, mutatesScore: true},

	// The scoreboard is gated on both account and score visibility, and is not
	// time-gated (you can look at the board before the CTF starts).
	ClassScoreboard:       {visGates: []VisKind{VisAccount, VisScore}},
	ClassScoreboardDetail: {visGates: []VisKind{VisAccount, VisScore}},

	ClassAccountList:   {visGates: []VisKind{VisAccount}},
	ClassAccountDetail: {visGates: []VisKind{VisAccount}},
	ClassAccountSelf:   {requiresAuth: true},
	// NOT ban-exempt: a banned user has no password-changing to do here. Only the wall that
	// would otherwise trap its own exit is lifted.
	ClassPasswordChange: {requiresAuth: true, exemptFromPasswordChange: true},

	ClassTeamEnrollment: {requiresAuth: true, modes: []account.Mode{account.ModeTeams}},
	ClassTeamCreate:     {requiresAuth: true, modes: []account.Mode{account.ModeTeams}},
	// The public team page: account visibility gates it like an account detail, and it
	// simply does not exist in users mode.
	ClassTeamDetail: {visGates: []VisKind{VisAccount}, modes: []account.Mode{account.ModeTeams}},

	ClassTokens: {requiresAuth: true, requiresVerified: true},
	ClassSSE:    {requiresAuth: true},
	// Notifications are broadcast to participants: a logged-in account may read the list and open
	// the stream, an anonymous caller may not. No visibility gate — there is no per-account
	// targeting, so there is nothing to hide, only a wall to be inside.
	ClassNotifications: {requiresAuth: true},

	ClassAdmin:           {adminOnly: true},
	ClassAdminScoreboard: {adminOnly: true},
	ClassStatistics:      {adminOnly: true},
	ClassExport:          {adminOnly: true},
}

func (c RouteClass) attrs() attrs { return classAttrs[c] }

// AvailableIn reports whether the route exists at all under this account mode. A route
// that does not exist in this mode 404s rather than 403s: a 403 would confirm the route
// is there, leaking the instance's mode to an anonymous caller.
func (c RouteClass) AvailableIn(m account.Mode) bool {
	modes := c.attrs().modes
	if len(modes) == 0 {
		return true
	}
	for _, allowed := range modes {
		if allowed == m {
			return true
		}
	}
	return false
}

func (c RouteClass) VisibilityGates() []VisKind     { return c.attrs().visGates }
func (c RouteClass) RequiresAuth() bool             { return c.attrs().requiresAuth }
func (c RouteClass) RequiresVerified() bool         { return c.attrs().requiresVerified }
func (c RouteClass) RequiresCompleteProfile() bool  { return c.attrs().requiresProfile }
func (c RouteClass) RequiresTeam() bool             { return c.attrs().requiresTeam }
func (c RouteClass) TimeGated() bool                { return c.attrs().timeGated }
func (c RouteClass) MutatesScore() bool             { return c.attrs().mutatesScore }
func (c RouteClass) AdminOnly() bool                { return c.attrs().adminOnly }
func (c RouteClass) ExemptFromBan() bool            { return c.attrs().exemptFromBan }
func (c RouteClass) ExemptFromPasswordChange() bool { return c.attrs().exemptFromPasswordChange }

var classNames = map[RouteClass]string{
	ClassUnknown: "unknown", ClassThemeAsset: "theme-asset", ClassSetup: "setup",
	ClassIndex: "index", ClassPages: "pages", ClassRegister: "register",
	ClassLogin: "login", ClassLogout: "logout", ClassReset: "reset", ClassConfirm: "confirm",
	ClassChallengeList: "challenge-list", ClassChallengeDetail: "challenge-detail",
	ClassChallengeAttempt: "challenge-attempt", ClassChallengeSolves: "challenge-solves",
	ClassHintUnlock: "hint-unlock", ClassSolutionUnlock: "solution-unlock",
	ClassScoreboard: "scoreboard", ClassScoreboardDetail: "scoreboard-detail",
	ClassAccountList: "account-list", ClassAccountDetail: "account-detail", ClassAccountSelf: "account-self",
	ClassPasswordChange: "password-change",
	ClassTeamEnrollment: "team-enrollment", ClassTeamCreate: "team-create", ClassTeamDetail: "team-detail",
	ClassTokens: "tokens", ClassSSE: "sse", ClassNotifications: "notifications",
	ClassAdmin: "admin", ClassAdminScoreboard: "admin-scoreboard",
	ClassStatistics: "statistics", ClassExport: "export",
}

func (c RouteClass) String() string {
	if s, ok := classNames[c]; ok {
		return s
	}
	return fmt.Sprintf("RouteClass(%d)", uint8(c))
}

// AllClasses is every route class, for the golden decision table. A new class that
// is not added here is not covered by the table, so the test that walks this list
// is what keeps the table honest.
func AllClasses() []RouteClass {
	out := make([]RouteClass, 0, len(classNames))
	for c := ClassThemeAsset; c <= ClassExport; c++ {
		out = append(out, c)
	}
	return out
}
