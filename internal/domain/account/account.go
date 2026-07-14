// Package account expresses the user/team duality — once: one type, one
// function, and one boot assertion. The mode is fixed at setup and immutable,
// which is what makes it a field rather than a query. A mode that could change
// mid-event would silently re-point every account-scoped query — and every score.
//
// This package imports nothing outside the standard library.
package account

import (
	"errors"
	"fmt"
)

// Mode is the instance's account model. It is chosen at setup and never changes.
// There is no code path in this product that writes it twice.
type Mode uint8

const (
	// ModeUsers: the account is the user. Solves, awards and standings key on user_id.
	ModeUsers Mode = iota
	// ModeTeams: the account is the team. Solves, awards and standings key on team_id,
	// and a user without a team cannot play.
	ModeTeams
)

var modeNames = map[Mode]string{ModeUsers: "users", ModeTeams: "teams"}

func (m Mode) String() string {
	if s, ok := modeNames[m]; ok {
		return s
	}
	return fmt.Sprintf("Mode(%d)", uint8(m))
}

// ErrUnknownMode is returned by ParseMode.
var ErrUnknownMode = errors.New("account: unknown user_mode")

// ParseMode maps the stored config value onto a Mode. It is called exactly once,
// at boot, when the config snapshot is built.
func ParseMode(s string) (Mode, error) {
	for m, name := range modeNames {
		if name == s {
			return m, nil
		}
	}
	return 0, fmt.Errorf("%w: %q (want %q or %q)", ErrUnknownMode, s, "users", "teams")
}

// ErrTeamless is returned when a user in teams mode has no team. In teams mode a
// teamless user has no account: they cannot solve, cannot score, and cannot appear
// on the board. That is not an error condition to be papered over with a zero value
// — it is a state the policy layer must handle (ReasonTeamRequired), and returning
// an error here is what forces every caller to.
var ErrTeamless = errors.New("account: user has no team, and in teams mode a user without a team has no account")

// An ID is an account identifier: a user id in users mode, a team id in teams
// mode. Every account-scoped query in the system takes one of these, and it is the
// only thing they take — no mode parameter travels with it, because by the time you
// hold an ID the mode has already been applied.
type ID int64

// A Membership is what the session actually knows about the caller: a user, and
// possibly a team.
type Membership struct {
	UserID int64
	// TeamID is nil when the user is not on a team. In users mode it is ignored.
	TeamID *int64
}

// Resolve collapses a Membership to the account that plays the game.
//
// This is the function. Every place in the product that needs to know "whose
// solve is this?" calls it, and nowhere else re-derives the answer. If you find
// yourself writing `if mode == teams { x = teamID } else { x = userID }` anywhere
// else in this codebase, delete it and call this.
func (m Mode) Resolve(mem Membership) (ID, error) {
	switch m {
	case ModeUsers:
		if mem.UserID == 0 {
			return 0, errors.New("account: no user id")
		}
		return ID(mem.UserID), nil

	case ModeTeams:
		if mem.TeamID == nil || *mem.TeamID == 0 {
			return 0, ErrTeamless
		}
		return ID(*mem.TeamID), nil

	default:
		return 0, fmt.Errorf("%w: %s", ErrUnknownMode, m)
	}
}

// Teamless reports whether this membership has no account under the mode. It is
// the policy layer's Principal.Teamless, computed here so there is one definition.
func (m Mode) Teamless(mem Membership) bool {
	_, err := m.Resolve(mem)
	return errors.Is(err, ErrTeamless)
}

// ErrModeMismatch is returned by AssertModeAtBoot.
var ErrModeMismatch = errors.New("account: configured user_mode contradicts the data in the database")

// AssertModeAtBoot is what makes Mode a trustworthy field rather than a
// hopeful one.
//
// The mode is immutable after setup — but "immutable" is a claim about our code,
// and the database can be restored, imported into, or hand-edited. If the config
// says `users` and the teams table has rows, then either the mode was flipped
// behind our back or an archive was imported into the wrong instance. Either way
// every account-scoped query in the process is now silently answering a different
// question than it was built to answer.
//
// So we refuse to boot. One comparison, at startup, once — against an entire class
// of silently-wrong scoreboards.
func AssertModeAtBoot(m Mode, teamCount int) error {
	if m == ModeUsers && teamCount > 0 {
		return fmt.Errorf("%w: user_mode is %q but the teams table holds %d rows — "+
			"this instance was set up in teams mode, or an archive was imported into the wrong instance. "+
			"Refusing to start: every account-scoped query would silently key on the wrong column",
			ErrModeMismatch, m, teamCount)
	}
	return nil
}
