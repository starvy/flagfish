package policy

import (
	"fmt"

	"github.com/starvy/flagfish/internal/domain/account"
)

// Reason names every way a request can be denied. It is the RFC 7807 `type` on
// the wire and the assertion key in the golden table — which means a policy change
// shows up as a diff in a named constant, not as a changed integer somewhere.
type Reason uint8

const (
	ReasonNone Reason = iota
	ReasonSetupIncomplete
	ReasonBanned
	ReasonTeamBanned
	ReasonPasswordChangeRequired
	ReasonNotFound
	ReasonAuthRequired
	ReasonAuthenticationRequired // the attempt path's 403-not-redirect variant
	ReasonAdminsOnly
	ReasonScoresHidden
	ReasonUnverified
	ReasonIncompleteProfile
	ReasonIncompleteTeamProfile
	ReasonTeamRequired
	ReasonAlreadyOnTeam
	ReasonTeamCreationDisabled
	ReasonCTFNotStarted
	ReasonCTFEnded
	ReasonPaused
	ReasonAlreadyAuthed
	ReasonAdminRequired
)

var reasonNames = map[Reason]string{
	ReasonNone: "", ReasonSetupIncomplete: "setup-incomplete", ReasonBanned: "banned",
	ReasonTeamBanned: "team-banned", ReasonPasswordChangeRequired: "password-change-required",
	ReasonNotFound: "not-found", ReasonAuthRequired: "auth-required",
	ReasonAuthenticationRequired: "authentication-required", ReasonAdminsOnly: "admins-only",
	ReasonScoresHidden: "scores-hidden", ReasonUnverified: "unverified",
	ReasonIncompleteProfile: "incomplete-profile", ReasonIncompleteTeamProfile: "incomplete-team-profile",
	ReasonTeamRequired: "team-required", ReasonAlreadyOnTeam: "already-on-team",
	ReasonTeamCreationDisabled: "team-creation-disabled", ReasonCTFNotStarted: "ctf-not-started",
	ReasonCTFEnded: "ctf-ended", ReasonPaused: "paused", ReasonAlreadyAuthed: "already-authed",
	ReasonAdminRequired: "admin-required",
}

func (r Reason) String() string {
	if s, ok := reasonNames[r]; ok {
		return s
	}
	return fmt.Sprintf("Reason(%d)", uint8(r))
}

// An Outcome carries both a status and a redirect, and the transport picks:
// JSON/SSE takes the status (RFC 7807); HTML takes the redirect if there is
// one, else the error page. The content-type fork lives in that one place, so
// the policy never has to know what the caller wanted.
type Outcome struct {
	Allow    bool
	Status   int // 403 | 404 | 0
	Redirect string
	Reason   Reason
}

// Denied is the inverse of Allow, spelled out so call sites read as English.
func (o Outcome) Denied() bool { return !o.Allow }

var (
	Allow        = Outcome{Allow: true}
	AuthRequired = Outcome{Status: 403, Redirect: "/login", Reason: ReasonAuthRequired}
	NotFound     = Outcome{Status: 404, Reason: ReasonNotFound}
)

// Decide is L1: the whole route policy gate, as one pure function of one struct.
//
// The evaluation order below is canonical. Reordering gates changes which error
// a denied caller sees (never allow/deny itself), so the order is part of the
// contract.
//
// Freeze is deliberately not here. It is not an allow/deny decision — it is a query
// parameter. See FreezeExempt.
func Decide(p Policy) Outcome {
	e, pr, r := p.E, p.P, p.R

	// 0. A route that declared no class is a wiring bug, and it fails closed:
	//    treating "no class" as "no gates" would let a forgotten annotation
	//    silently publish an endpoint.
	if r.Class == ClassUnknown {
		return NotFound
	}

	// 1. Setup.
	if !e.SetupDone && r.Class != ClassSetup && r.Class != ClassThemeAsset {
		return Outcome{Redirect: "/setup", Reason: ReasonSetupIncomplete}
	}
	if e.SetupDone && r.Class == ClassSetup {
		return NotFound // setup is a one-time route; once done, it does not exist
	}

	// 2. authn has already happened: the Principal is resolved from the session
	//    cookie or the API token before we are called. Which one it was is not
	//    knowable from here, and that is the point.

	// 3. The ban wall. Reads pr.Banned regardless of how the principal was
	//    authenticated — see PolicyBanCoversTokenAuth. There is exactly one ban
	//    check in this product and it is here, which is what makes a token-auth
	//    bypass unreachable.
	if pr.Authed && !r.Class.ExemptFromBan() {
		if pr.Banned {
			return Outcome{Status: 403, Reason: ReasonBanned}
		}
		if pr.TeamBanned {
			return Outcome{Status: 403, Reason: ReasonTeamBanned}
		}
	}

	// 4. Forced password change. The destination is the logged-in change form, not the
	//    emailed-token reset: the caller is authenticated and knows the password they
	//    just used, so sending them somewhere that needs a link out of a mailbox would
	//    make a broken mailer into a lockout.
	if pr.Authed && pr.ForcePasswordChange && !r.Class.ExemptFromPasswordChange() {
		return Outcome{Redirect: "/change-password", Reason: ReasonPasswordChangeRequired}
	}

	// 5. The route may not exist in this mode at all.
	if !r.Class.AvailableIn(e.Mode) {
		return NotFound
	}

	// 6. Admin surface.
	if r.Class.AdminOnly() && !pr.IsAdmin {
		return Outcome{Status: 403, Reason: ReasonAdminRequired}
	}

	// 7. Visibility.
	for _, kind := range r.Class.VisibilityGates() {
		if o := checkVis(kind, e, pr); o.Denied() {
			return o
		}
	}

	// 8. Authentication.
	if r.Class.RequiresAuth() && !pr.Authed {
		if r.Class == ClassChallengeAttempt {
			// The attempt path 403s an anonymous caller rather than redirecting:
			// it is an API, and a 302 to /login is not an answer a scoreboard bot
			// can act on.
			return Outcome{Status: 403, Reason: ReasonAuthenticationRequired}
		}
		return AuthRequired
	}
	// Registration is for people who do not have an account.
	if r.Class == ClassRegister && pr.Authed {
		return Outcome{Redirect: "/challenges", Reason: ReasonAlreadyAuthed}
	}

	// 9. Email verification. The `pr.Authed &&` conjunct is load-bearing:
	//    see PolicyUnverifiedStricterThanAnonymous.
	if e.VerifyEmails && pr.Authed && !pr.IsAdmin && !pr.Verified && r.Class.RequiresVerified() {
		return Outcome{Status: 403, Redirect: "/confirm", Reason: ReasonUnverified}
	}

	// 10. Profile completeness.
	if r.Class.RequiresCompleteProfile() && pr.Authed && !pr.IsAdmin {
		if !pr.ProfileComplete {
			return Outcome{Redirect: "/settings", Reason: ReasonIncompleteProfile}
		}
		if e.Mode == account.ModeTeams && !pr.Teamless && !pr.TeamProfileComplete {
			return Outcome{Status: 403, Reason: ReasonIncompleteTeamProfile}
		}
	}

	// 11. Team membership.
	if r.Class.RequiresTeam() && e.Mode == account.ModeTeams && pr.Teamless && !pr.IsAdmin {
		return Outcome{Status: 403, Redirect: "/team", Reason: ReasonTeamRequired}
	}
	if r.Class == ClassTeamCreate {
		if !e.TeamCreation && !pr.IsAdmin {
			return Outcome{Status: 403, Reason: ReasonTeamCreationDisabled}
		}
		if !pr.Teamless {
			return Outcome{Status: 403, Reason: ReasonAlreadyOnTeam}
		}
	}

	// 12. The clock.
	//
	// The switch is exhaustive on purpose: a new Phase must trip the `exhaustive`
	// linter here rather than fall through to "the CTF is open" — wrong in the
	// permissive direction is the only direction that matters.
	if r.Class.TimeGated() && !pr.IsAdmin {
		switch e.Phase {
		case PhaseRunning:
			// The CTF is live. Fall through to the remaining gates.

		case PhaseEnded:
			// view_after_ctf reopens the challenges for reading, never for scoring: the
			// standings are final the moment the clock stops. Without this guard the
			// courtesy setting that lets people browse afterwards also lets them keep
			// solving — and taking first bloods — against a field that has gone home.
			if r.Class.MutatesScore() || !e.ViewAfterCTF {
				return Outcome{Status: 403, Reason: ReasonCTFEnded}
			}

		case PhaseBeforeStart:
			if e.Mode == account.ModeTeams && pr.Teamless {
				// Redirect, not 403: before the CTF starts, a teamless user's
				// problem is that they have no team, and telling them "not started"
				// sends them away to wait instead of to the enrollment page.
				return Outcome{Redirect: "/team", Reason: ReasonTeamRequired}
			}
			return Outcome{Status: 403, Reason: ReasonCTFNotStarted}
		}
	}

	// 12b. The enrollment window, which is the clock read the other way round: open before the
	//      start rather than closed, and shut at the end regardless of view_after_ctf. Admins
	//      are exempt here for the same reason they are exempt above — an organiser fixing a
	//      roster after the fact is doing their job, and it lands in the audit trail.
	if r.Class.ClosesAtEnd() && !pr.IsAdmin && e.Phase == PhaseEnded {
		return Outcome{Status: 403, Reason: ReasonCTFEnded}
	}

	// 13. Pause. Only the attempt class is gated — see PolicyPauseDoesNotBlockUnlocks.
	//     Preview lifts the gate only for an admin: it arrives straight off the query
	//     string, so honouring it for anyone would make `?preview=1` a pause bypass.
	if r.Class == ClassChallengeAttempt && e.Paused && (!r.Preview || !pr.IsAdmin) {
		return Outcome{Status: 403, Reason: ReasonPaused}
	}

	// L1 ends here. L2 resource guards (challenge state, prerequisites, ownership,
	// captaincy, hint affordability) run in the handler, against the target row.
	return Allow
}

// checkVis is the visibility gate.
//
// The asymmetry is deliberate (PolicyAdminsOnlyHidesExistenceForAccountsNotChallenges):
// `admins` visibility yields 403 on challenges but 404 on scores and accounts —
// accounts and scores hide their existence, challenges are merely gated.
func checkVis(kind VisKind, e Event, pr Principal) Outcome {
	switch e.visFor(kind) {
	case VisPublic:
		return Allow

	case VisPrivate:
		if kind == VisRegistration {
			// There is nobody to authenticate: registration is the route you take
			// because you have no account. `private` therefore means the form does
			// not exist — 404, not "log in first".
			return NotFound
		}
		if pr.Authed {
			return Allow
		}
		return AuthRequired

	case VisHidden: // score only
		if pr.IsAdmin {
			return Allow
		}
		return Outcome{Status: 403, Reason: ReasonScoresHidden}

	case VisAdmins:
		if pr.IsAdmin {
			return Allow
		}
		if kind == VisChallenge {
			if pr.Authed {
				return Outcome{Status: 403, Reason: ReasonAdminsOnly}
			}
			return AuthRequired
		}
		return NotFound // scores + accounts hide their existence

	case VisMLC: // registration only
		// PolicyRegistrationMLCTreatedAsPrivate: the registration form is
		// unavailable and accounts are created only via the MLC OAuth callback.
		// So: 404, as `private` does.
		return NotFound
	}
	return NotFound // unknown setting: fail closed
}
