// Package policy is the single place this product decides who may see and do what.
//
// Visibility is enforced in four layers:
//
//	L1 — Route policy gate   Decide(Policy) -> Outcome. Pure. No DB. This file's job.
//	L2 — Resource guards     per-row checks needing the target row (challenge state,
//	                         prereqs, ownership, captaincy). In the handler.
//	L3 — SQL predicates      six WHERE-clause fragments, inside the sqlc queries.
//	L4 — Field redaction     view masks + score/place/solve-count nulling. At serialization.
//
// The L2 checks need the target resource rather than the principal. They live in
// the handler, and keeping them out of here is precisely what keeps Decide a pure
// function.
//
// This package's only non-stdlib import is its sibling internal/domain/account,
// because the user/team duality is expressed exactly once in this codebase.
package policy

import (
	"fmt"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
)

// Phase is where the clock is, relative to the event. Three-valued, not a bool:
// `during_ctf_time_only` distinguishes before-start from after-end (it consults
// view_after_ctf only in the latter, and in teams mode a teamless user before start
// is REDIRECTED to enrollment rather than 403'd). A single "is the CTF running"
// boolean cannot express that.
type Phase uint8

const (
	PhaseBeforeStart Phase = iota
	PhaseRunning
	PhaseEnded
)

func (p Phase) String() string {
	switch p {
	case PhaseBeforeStart:
		return "before-start"
	case PhaseRunning:
		return "running"
	case PhaseEnded:
		return "ended"
	}
	return fmt.Sprintf("Phase(%d)", uint8(p))
}

// PhaseAt derives the phase from the configured window.
//
// Both bounds are strict: the event has started once now is
// strictly after `start`, and ended once now is strictly after `end`. An instant
// exactly equal to a bound is inside the event.
func PhaseAt(now time.Time, start, end *time.Time) Phase {
	if start != nil && !now.After(*start) {
		return PhaseBeforeStart
	}
	if end != nil && now.After(*end) {
		return PhaseEnded
	}
	return PhaseRunning
}

// Vis is a visibility setting's value.
type Vis uint8

const (
	VisPublic  Vis = iota // anyone
	VisPrivate            // any authenticated caller
	VisAdmins             // admins only
	VisHidden             // nobody but admins. Score only.
	VisMLC                // registration only: account creation happens via the MLC OAuth callback
)

func (v Vis) String() string {
	switch v {
	case VisPublic:
		return "public"
	case VisPrivate:
		return "private"
	case VisAdmins:
		return "admins"
	case VisHidden:
		return "hidden"
	case VisMLC:
		return "mlc"
	}
	return fmt.Sprintf("Vis(%d)", uint8(v))
}

// ParseVis maps a stored config value onto a Vis. Which values are legal depends
// on the key: `hidden` is score-only and `mlc` is registration-only. The config
// schema enforces that at load; this only parses.
func ParseVis(s string) (Vis, error) {
	switch s {
	case "public":
		return VisPublic, nil
	case "private":
		return VisPrivate, nil
	case "admins":
		return VisAdmins, nil
	case "hidden":
		return VisHidden, nil
	case "mlc":
		return VisMLC, nil
	}
	return 0, fmt.Errorf("policy: unknown visibility %q", s)
}

// VisKind selects which visibility setting a route class is gated on.
type VisKind uint8

const (
	VisChallenge VisKind = iota
	VisScore
	VisAccount
	VisRegistration
)

// Surface is the call site's namespace: the public API, or the admin API.
//
// The freeze exemption is a property of the surface, not of the caller — see
// FreezeExempt. Nobody can widen it by editing a default argument, because there
// is no default argument.
type Surface uint8

const (
	SurfacePublic Surface = iota
	SurfaceAdmin
)

// Event is the instance's configuration, as the policy layer sees it. It is
// derived from the config snapshot (an atomic.Pointer: a read is a pointer
// dereference — no cache, no TTL, no invalidation graph).
type Event struct {
	Mode      account.Mode
	SetupDone bool

	ChallengeVis    Vis
	ScoreVis        Vis
	AccountVis      Vis
	RegistrationVis Vis

	VerifyEmails bool
	ViewAfterCTF bool

	Phase  Phase
	Paused bool
	// FreezeAt is nil when there is no freeze. It is not an input to Decide —
	// freeze is not an allow/deny decision, it is a query parameter (see FreezeExempt).
	FreezeAt *time.Time

	TeamCreation bool
}

func (e Event) visFor(k VisKind) Vis {
	switch k {
	case VisChallenge:
		return e.ChallengeVis
	case VisScore:
		return e.ScoreVis
	case VisAccount:
		return e.AccountVis
	case VisRegistration:
		return e.RegistrationVis
	}
	return VisAdmins // unknown kind: deny. An unreachable default must fail closed.
}

// Principal is everything about the caller, and nothing about the resource.
//
// It is resolved once per request, from either credential — session cookie or API
// token — before Decide runs. That is what makes a token-auth ban bypass
// unreachable: there is one ban check and one principal, so a banned user with a
// valid API token hits the same wall a cookie does (PolicyBanCoversTokenAuth).
//
// There is deliberately no Hidden field: hiddenness is an L3 SQL predicate, never
// an L1 input. A hidden account authenticates, plays and solves normally, and is
// merely absent from listings, standings and caps (PolicyHiddenIsNotBlocked). The
// absence of the field is the encoding of that policy — do not add one.
type Principal struct {
	Authed  bool
	IsAdmin bool

	Verified   bool
	Banned     bool
	TeamBanned bool
	Teamless   bool // teams mode && the user has no team. account.Mode.Teamless.

	ProfileComplete     bool
	TeamProfileComplete bool
	ForcePasswordChange bool

	UserID    int64
	AccountID account.ID
}

// Request is the call site.
type Request struct {
	Class   RouteClass
	Surface Surface
	// AdminView is ?view=admin. It is not the same thing as IsAdmin — the
	// difference is what FreezeExempt exists to draw.
	AdminView bool
	// Preview is ?preview on the admin challenge-attempt path.
	Preview bool
}

// Policy is the complete input to the decision. If a gate needs something that is
// not in here, it is not an L1 gate.
type Policy struct {
	E Event
	P Principal
	R Request
}
