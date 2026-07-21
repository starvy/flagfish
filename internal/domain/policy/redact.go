package policy

import "time"

// L4 — field redaction.
//
// This is a different mechanism from the view mask, and it must stay separate.
// The view mask decides which fields exist in a response shape (and is generated
// from the entity's `views` tags, so the same mask governs writes). This decides
// which already-fetched values get nulled out. Conflating the two produces a
// response type that varies by role, which OpenAPI cannot express.

// A Redactor is derived from the same Policy that fed Decide. One source of
// truth, two consumers — so a visibility setting cannot be interpreted one way by
// the gate and another way by the serializer.
type Redactor struct {
	ScoresVisible   bool
	AccountsVisible bool
}

// NewRedactor computes the two visibility predicates:
// public -> true, private -> authed, admins -> is_admin, hidden -> false (score only).
func NewRedactor(p Policy) Redactor {
	return Redactor{
		ScoresVisible:   visible(p.E.ScoreVis, p.P),
		AccountsVisible: visible(p.E.AccountVis, p.P),
	}
}

func visible(v Vis, pr Principal) bool {
	switch v {
	case VisPublic:
		return true
	case VisPrivate:
		return pr.Authed
	case VisAdmins:
		return pr.IsAdmin
	case VisHidden:
		return false // even for admins
	case VisMLC:
		// Registration-only (see policy.go). It is never a score or account
		// visibility, and if it somehow lands here the safe answer is "hidden".
		return false
	}
	return false
}

// AccountFields are the nullable fields of an account row on the wire.
//
// They are pointers because the wire format is `null` — not 0, and not absent.
// A scoreboard that reports a redacted score as 0 is not redacted, it is wrong,
// and it is wrong in a way that looks plausible. This is the exact failure class
// sqlc was chosen to avoid, and the pointer is what makes it unrepresentable.
type AccountFields struct {
	Score *int
	Place *int
}

// Account nulls score and place when scores are not visible.
func (r Redactor) Account(f *AccountFields) {
	if !r.ScoresVisible {
		f.Score, f.Place = nil, nil
	}
}

// Self applies the self view's redaction, which is deliberately weaker.
//
// PolicyOwnScoreAlwaysVisible: GET /users/me returns a live `score` —
// freeze-ignoring and visibility-ignoring — while `place` stays gated. A user
// can sum their own solves anyway, so hiding the score buys nothing; they
// cannot derive their own rank, so that stays hidden. Only Place is redacted.
func (r Redactor) Self(f *AccountFields) {
	if !r.ScoresVisible {
		f.Place = nil
	}
}

// ChallengeSolveCount is nulled when scores and accounts are both invisible.
// Note the conjunction: solve counts leak nothing unless both are hidden.
func (r Redactor) ChallengeSolveCount(count *int) *int {
	if r.ScoresVisible && r.AccountsVisible {
		return count
	}
	return nil // null, not 0
}

// A SolveEntry is one row of a per-challenge solve list: who solved it, for how much, when.
type SolveEntry struct {
	Name  string
	Value int32
	Date  time.Time
}

// ChallengeSolveList applies the same conjunction ChallengeSolveCount does, and has to: the
// length of this list IS the solve count, so serving rows beside a nulled count would hand back
// by difference the very number the count withholds. That is also why redaction here is omission
// rather than anonymised rows — an anonymous row still counts, and a timestamped one still says
// when the solve landed.
func (r Redactor) ChallengeSolveList(solves []SolveEntry) []SolveEntry {
	if r.ScoresVisible && r.AccountsVisible {
		return solves
	}
	return nil
}
