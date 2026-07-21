package adminops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
)

var (
	// ErrUserNotOnTeam separates "that roster does not contain them" from "no such user" — an
	// organizer acting on a stale page needs to know which of the two they got.
	ErrUserNotOnTeam = errors.New("adminops: user is not on that team")
	// ErrTeamFull is the team_size cap, distinct from ErrTeamCapReached which is num_teams.
	ErrTeamFull = errors.New("adminops: the team_size cap is reached")
	// ErrSameTeam refuses a move whose source and destination match, rather than churning the
	// roster and the captain's seat to land exactly where it started.
	ErrSameTeam = errors.New("adminops: user is already on that team")
	// ErrNotTeamsMode refuses roster edits on an instance where the account is the user: there is
	// no roster to repair, and pretending otherwise would write a team_id nothing reads.
	ErrNotTeamsMode = errors.New("adminops: the instance is not in teams mode")
)

// TeamMember is one row of the admin roster view.
type TeamMember struct {
	UserID  int64
	Name    string
	Email   string
	Captain bool
	Banned  bool
	Hidden  bool
}

// ListTeamMembers returns a team's roster. A missing team is an error rather than an empty list, so
// a mistyped id cannot read as a team that lost its members.
func (s *Service) ListTeamMembers(ctx context.Context, mode account.Mode, teamID int64) ([]TeamMember, error) {
	if mode != account.ModeTeams {
		return nil, ErrNotTeamsMode
	}
	present, err := s.q.AdminTeamExists(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("adminops: look up team %d: %w", teamID, err)
	}
	if !present {
		return nil, fmt.Errorf("%w: id=%d", ErrTeamNotFound, teamID)
	}
	rows, err := s.q.AdminListTeamMembers(ctx, &teamID)
	if err != nil {
		return nil, fmt.Errorf("adminops: list team %d members: %w", teamID, err)
	}
	out := make([]TeamMember, len(rows))
	for i := range rows {
		r := &rows[i]
		out[i] = TeamMember{
			UserID: r.ID, Name: r.Name, Email: r.Email,
			Captain: r.Captain, Banned: r.Banned, Hidden: r.Hidden,
		}
	}
	return out, nil
}

// RemoveTeamMember takes one player off a team and leaves them teamless. The team's ledger is not
// touched: solves, awards and submissions stamped that team_id when they happened, and they stay
// exactly as they are — the scoreboard after this call reads the same as before it.
func (s *Service) RemoveTeamMember(ctx context.Context, actor audit.Actor, mode account.Mode, teamID, userID int64) error {
	if mode != account.ModeTeams {
		return ErrNotTeamsMode
	}
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		return detachMember(ctx, q, teamID, userID)
	})
}

// MoveTeamMember reassigns a player from one team to another.
//
// History is deliberately left alone. Every ledger row carries the team_id it was written with, so
// points already earned stay credited to the team that earned them and the board does not move.
// Only the player's future attribution changes.
func (s *Service) MoveTeamMember(ctx context.Context, actor audit.Actor, mode account.Mode, fromTeamID, userID, toTeamID int64) error {
	if mode != account.ModeTeams {
		return ErrNotTeamsMode
	}
	if fromTeamID == toTeamID {
		return fmt.Errorf("%w: id=%d", ErrSameTeam, toTeamID)
	}
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		if err := detachMember(ctx, q, fromTeamID, userID); err != nil {
			return err
		}
		// A direct team-to-team write is refused by the switch guard, so a move is the leave and
		// the join a player would make themselves, run back to back in one transaction. The join
		// leg is the very statement the self-serve join uses: the caps trigger fires on it, takes
		// the registration advisory lock, and decides team_size inside the same statement. There is
		// no room for a check-then-update race because nothing here checks the size at all.
		n, err := q.EnrollUser(ctx, db.EnrollUserParams{TeamID: &toTeamID, UserID: userID})
		if err != nil {
			return fmt.Errorf("adminops: move user %d to team %d: %w", userID, toTeamID, moveConstraint(err))
		}
		if n == 0 {
			// The detach above locked this row and left team_id NULL, so the join cannot miss.
			return fmt.Errorf("adminops: move user %d to team %d: enrolment matched no row", userID, toTeamID)
		}
		// Mirrors the join path: a lone arrival adopts an empty seat, so a move can never leave a
		// populated team captainless.
		if err := q.AdoptCaptainlessTeam(ctx, db.AdoptCaptainlessTeamParams{UserID: &userID, TeamID: toTeamID}); err != nil {
			return fmt.Errorf("adminops: adopt captaincy of team %d: %w", toTeamID, err)
		}
		return nil
	})
}

// detachMember nulls a membership and settles the captain's seat behind it.
func detachMember(ctx context.Context, q *db.Queries, teamID, userID int64) error {
	n, err := q.AdminDetachMember(ctx, db.AdminDetachMemberParams{UserID: userID, TeamID: &teamID})
	if err != nil {
		return fmt.Errorf("adminops: remove user %d from team %d: %w", userID, teamID, err)
	}
	if n == 0 {
		return zeroRowReason(ctx, q, teamID, userID)
	}
	// After the detach, never before: the seat is filled from the remaining members, so running
	// this first would hand it back to the person walking out. NULL when the team is now empty.
	if err := q.ReassignCaptainAfterLeave(ctx, db.ReassignCaptainAfterLeaveParams{TeamID: teamID, UserID: &userID}); err != nil {
		return fmt.Errorf("adminops: reassign captain of team %d: %w", teamID, err)
	}
	return nil
}

// zeroRowReason says which fact was false when the detach matched nothing. It runs only to shape an
// error, so the extra reads cost nothing on the path that succeeds.
func zeroRowReason(ctx context.Context, q *db.Queries, teamID, userID int64) error {
	present, err := q.AdminTeamExists(ctx, teamID)
	if err != nil {
		return fmt.Errorf("adminops: look up team %d: %w", teamID, err)
	}
	if !present {
		return fmt.Errorf("%w: id=%d", ErrTeamNotFound, teamID)
	}
	if _, err := q.GetUserByID(ctx, userID); errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
	} else if err != nil {
		return fmt.Errorf("adminops: look up user %d: %w", userID, err)
	}
	return fmt.Errorf("%w: user=%d team=%d", ErrUserNotOnTeam, userID, teamID)
}

// moveConstraint names the two refusals the join leg can raise.
func moveConstraint(err error) error {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return err
	}
	switch {
	case pg.Code == "23503":
		return ErrTeamNotFound
	// On an UPDATE the caps trigger only ever weighs team_size — how many users exist is an
	// INSERT question, so a full team is the one thing this can mean.
	case pg.Code == "23514" && pg.ConstraintName == "users_caps":
		return ErrTeamFull
	}
	return err
}
