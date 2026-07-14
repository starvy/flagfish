package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/db"
)

var (
	ErrTeamNameTaken  = errors.New("accounts: team name is already taken")
	ErrTeamCapReached = errors.New("accounts: team registration is full")
	ErrTeamFull       = errors.New("accounts: team is full")
	ErrAlreadyOnTeam  = errors.New("accounts: user is already on a team")

	// ErrTeamJoinDenied covers "no such team" and "wrong password" alike, for the same
	// reason ErrBadCredentials does: the difference is an enumeration oracle.
	ErrTeamJoinDenied = errors.New("accounts: team join denied")

	ErrTeamNotFound  = errors.New("accounts: team not found")
	ErrNotOnTeam     = errors.New("accounts: user is not on a team")
	ErrTeamHasScored = errors.New("accounts: cannot leave a team that has solves")
)

// TeamMember is one roster row, with the member's contribution read from the stamped
// solves ledger.
type TeamMember struct {
	UserID     int64
	Name       string
	Captain    bool
	SolveCount int64
	Points     int64
}

// Team is a team profile: the public view, or the caller's own.
type Team struct {
	ID          int64
	Name        string
	Website     *string
	Affiliation *string
	Country     *string
	Score       int64
	CreatedAt   time.Time
	IsCaptain   bool // only meaningful on the own-team view
	Members     []TeamMember
}

// CreateTeam creates a team with the caller as captain and sole member, atomically —
// there is no instant at which the team exists captainless or the creator is teamless.
//
// The unique name index and the num_teams caps trigger arbitrate concurrent creates the
// same way registration's constraints do: on the INSERT, never in a prior check.
func (s *Service) CreateTeam(ctx context.Context, userID int64, name, password string) (Team, error) {
	// An empty join password stays NULL; the hasher refuses empty strings on purpose.
	var hash *string
	if password != "" {
		h, err := Hash(password)
		if err != nil {
			return Team{}, err
		}
		hash = &h
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Team{}, fmt.Errorf("accounts: create team: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	row, err := q.CreateTeam(ctx, db.CreateTeamParams{
		Name: name, PasswordHash: hash, CaptainID: &userID,
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			switch pg.Code {
			case "23505":
				return Team{}, ErrTeamNameTaken
			case "23514":
				return Team{}, ErrTeamCapReached
			}
		}
		return Team{}, fmt.Errorf("accounts: create team: %w", err)
	}

	if err := s.enroll(ctx, q, row.ID, userID); err != nil {
		return Team{}, fmt.Errorf("accounts: create team: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Team{}, fmt.Errorf("accounts: create team: commit: %w", err)
	}
	return s.OwnTeam(ctx, userID)
}

// JoinTeam enrolls the caller in the team named, if the password verifies. The team_size
// cap is the caps trigger's job: two users racing for the last slot serialize in the
// database, and the loser gets a check_violation, not a seat.
func (s *Service) JoinTeam(ctx context.Context, userID int64, name, password string) (Team, error) {
	t, err := s.q.GetTeamForJoin(ctx, name)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Same cost and same answer as a wrong password, so team names cannot be
		// enumerated by timing or by message.
		Verify(dummyHash, password)
		return Team{}, ErrTeamJoinDenied
	case err != nil:
		return Team{}, fmt.Errorf("accounts: join team: %w", err)
	}

	if t.Banned {
		Verify(dummyHash, password)
		return Team{}, ErrTeamJoinDenied
	}

	if t.PasswordHash == nil {
		// A team created without a join password admits only an empty one.
		if password != "" {
			return Team{}, ErrTeamJoinDenied
		}
	} else {
		ok, rehash := Verify(*t.PasswordHash, password)
		if !ok {
			return Team{}, ErrTeamJoinDenied
		}
		if rehash {
			s.rehashTeamPassword(ctx, t.ID, password)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Team{}, fmt.Errorf("accounts: join team: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	if err := s.enroll(ctx, q, t.ID, userID); err != nil {
		return Team{}, fmt.Errorf("accounts: join team: %w", err)
	}
	// A sole member adopts a captainless team, so a team whose captain's account was
	// deleted does not stay unmanageable forever.
	if err := q.AdoptCaptainlessTeam(ctx, db.AdoptCaptainlessTeamParams{
		UserID: &userID, TeamID: t.ID,
	}); err != nil {
		return Team{}, fmt.Errorf("accounts: join team: adopt captainless: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Team{}, fmt.Errorf("accounts: join team: commit: %w", err)
	}
	return s.OwnTeam(ctx, userID)
}

// enroll flips the user onto the team and translates the two ways the database can say no.
func (s *Service) enroll(ctx context.Context, q *db.Queries, teamID, userID int64) error {
	rows, err := q.EnrollUser(ctx, db.EnrollUserParams{TeamID: &teamID, UserID: userID})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23514" {
			return ErrTeamFull
		}
		return fmt.Errorf("enroll user %d: %w", userID, err)
	}
	if rows == 0 {
		return ErrAlreadyOnTeam
	}
	return nil
}

func (s *Service) rehashTeamPassword(ctx context.Context, teamID int64, password string) {
	upgraded, err := Hash(password)
	if err == nil {
		err = s.q.UpdateTeamPasswordHash(ctx, db.UpdateTeamPasswordHashParams{
			TeamID: teamID, PasswordHash: &upgraded,
		})
	}
	if err != nil {
		// The old hash still verifies; an upgrade that failed is an operational note,
		// not this player's problem.
		s.log.ErrorContext(ctx, "team password rehash failed", "error", err, "team_id", teamID)
	}
}

// TeamProfile is the public team page. Hidden and banned teams do not exist here, and
// hidden or banned members are absent from the roster, matching the solve lists.
func (s *Service) TeamProfile(ctx context.Context, teamID int64) (Team, error) {
	row, err := s.q.GetTeamPublicProfile(ctx, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, fmt.Errorf("%w: id=%d", ErrTeamNotFound, teamID)
	} else if err != nil {
		return Team{}, fmt.Errorf("accounts: team profile: %w", err)
	}

	members, err := s.teamMembers(ctx, teamID, false)
	if err != nil {
		return Team{}, fmt.Errorf("accounts: team profile: %w", err)
	}

	return Team{
		ID: row.ID, Name: row.Name,
		Website: row.Website, Affiliation: row.Affiliation, Country: row.Country,
		Score: row.Score, CreatedAt: row.CreatedAt.Time, Members: members,
	}, nil
}

// OwnTeam is the caller's team, roster unmasked — it is their own.
func (s *Service) OwnTeam(ctx context.Context, userID int64) (Team, error) {
	row, err := s.q.GetOwnTeam(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, ErrNotOnTeam
	} else if err != nil {
		return Team{}, fmt.Errorf("accounts: own team: %w", err)
	}

	members, err := s.teamMembers(ctx, row.ID, true)
	if err != nil {
		return Team{}, fmt.Errorf("accounts: own team: %w", err)
	}

	return Team{
		ID: row.ID, Name: row.Name,
		Website: row.Website, Affiliation: row.Affiliation, Country: row.Country,
		Score: row.Score, CreatedAt: row.CreatedAt.Time,
		IsCaptain: row.CaptainID != nil && *row.CaptainID == userID,
		Members:   members,
	}, nil
}

func (s *Service) teamMembers(ctx context.Context, teamID int64, includeMasked bool) ([]TeamMember, error) {
	rows, err := s.q.ListTeamMembers(ctx, db.ListTeamMembersParams{
		TeamID: &teamID, IncludeMasked: includeMasked,
	})
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	members := make([]TeamMember, 0, len(rows))
	for _, r := range rows {
		members = append(members, TeamMember{
			UserID: r.ID, Name: r.Name, Captain: r.Captain,
			SolveCount: r.SolveCount, Points: r.Points,
		})
	}
	return members, nil
}

// LeaveTeam removes the caller from their team.
//
// Departure is forbidden once the team has any solves: solves stamp team_id, so a roster
// that can shrink after scoring would leave the board attributing points to people who
// were never asked. The check is in the UPDATE's WHERE clause, so it cannot be raced past.
// If the captain leaves, the seat passes to the lowest-id remaining member.
func (s *Service) LeaveTeam(ctx context.Context, userID int64) error {
	u, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("accounts: leave team: %w", err)
	}
	if u.TeamID == nil {
		return ErrNotOnTeam
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("accounts: leave team: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	rows, err := q.LeaveTeam(ctx, db.LeaveTeamParams{UserID: userID, TeamID: u.TeamID})
	if err != nil {
		return fmt.Errorf("accounts: leave team: %w", err)
	}
	if rows == 0 {
		return ErrTeamHasScored
	}

	if err := q.ReassignCaptainAfterLeave(ctx, db.ReassignCaptainAfterLeaveParams{
		TeamID: *u.TeamID, UserID: &userID,
	}); err != nil {
		return fmt.Errorf("accounts: leave team: reassign captain: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("accounts: leave team: commit: %w", err)
	}
	return nil
}
