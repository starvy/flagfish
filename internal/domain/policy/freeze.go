package policy

import "time"

// FreezeExempt is the only place in this product where the freeze exemption is
// computed.
//
// "Admins always see live data" is false, and believing it is how you leak the
// frozen scoreboard: the exemption is a property of the surface, not of the
// caller — pr.IsAdmin appears in exactly one arm, the one class that is
// genuinely role-driven. Collecting it here means nobody can widen it by
// editing a default argument, because there are no default arguments.
//
// The bool returned feeds the freeze SQL predicate as @freeze_exempt. It is not
// is_admin, and any query that passes is_admin there has reintroduced the leak.
func FreezeExempt(p Policy) bool {
	pr, r := p.P, p.R

	switch r.Class {
	case ClassScoreboard:
		// The public board is frozen for everyone, admins included. An admin who wants
		// to peek at the live standings during a freeze asks for it with ?preview; a
		// non-admin's ?preview is not an exemption, so preview is never a freeze bypass.
		return r.Surface == SurfaceAdmin || (pr.IsAdmin && r.Preview)

	case ClassScoreboardDetail, ClassAccountDetail, ClassAccountList:
		// The public surface is frozen for everyone, admins included. An admin who
		// wants live data goes to the admin surface, where the URL says so.
		return r.Surface == SurfaceAdmin

	case ClassChallengeList:
		// ?view=admin, not is_admin.
		return r.AdminView

	case ClassChallengeDetail:
		// Frozen even for admins.
		return false

	case ClassChallengeSolves:
		// The one genuinely role-driven site: freeze is forced on for non-admins,
		// and off for admins unless ?preview.
		return pr.IsAdmin && !r.Preview

	case ClassStatistics, ClassExport, ClassAdminScoreboard:
		return true

	default:
		// Everything else: frozen. A route that is not listed above has not thought
		// about freeze, and the safe answer for a route that has not thought about
		// freeze is to hide the data the freeze exists to hide.
		return false
	}
}

// Frozen reports whether a freeze is configured and applies to this call. It is
// the value the handler passes to its query alongside E.FreezeAt.
func Frozen(p Policy) bool {
	return p.E.FreezeAt != nil && !FreezeExempt(p)
}

// SuppressedByFreeze reports whether an event that happened at `at` must not be
// announced, because doing so would leak exactly what the frozen scoreboard hides.
// The horizon matches the scoreboard's own cutoff, which is strict: a solve landing
// at the freeze instant is already hidden from the board, so it is suppressed too.
// A nil freezeAt means no freeze is configured and nothing is suppressed.
func SuppressedByFreeze(freezeAt *time.Time, at time.Time) bool {
	return freezeAt != nil && !at.Before(*freezeAt)
}
