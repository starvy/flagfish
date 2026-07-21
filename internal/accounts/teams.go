package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/starvy/flagfish/internal/audit"
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

	ErrNotCaptain     = errors.New("accounts: only the captain can edit the team")
	ErrTeamEmailTaken = errors.New("accounts: team email is already in use")

	// ErrJoinSecretTooShort refuses a team whose roster would be protected by its name alone.
	// Team names are printed on the scoreboard, so a weak join secret is no secret at all.
	ErrJoinSecretTooShort = fmt.Errorf("accounts: a team join password must be at least %d characters", MinJoinSecretLen)

	// ErrTargetNotMember is a roster action aimed at someone who is not on the captain's team.
	ErrTargetNotMember = errors.New("accounts: target is not a member of your team")
	// ErrCannotKickSelf is the captain trying to kick themselves — leave or transfer instead.
	ErrCannotKickSelf = errors.New("accounts: the captain cannot kick themselves")
	// ErrTeamHasHistory is a disband refused by the ledger's RESTRICT foreign keys: the team has
	// submissions, solves, awards, or hint unlocks and can only be retired by hide/ban.
	ErrTeamHasHistory = errors.New("accounts: cannot disband a team with a scoreboard history")
)

// MinJoinSecretLen is the shortest join password a team may be created with. It matches the
// minimum on a user's own password: one number for a reader to remember, and a join secret
// guards no less than a login does — it is the whole wall around a team's solves and hints.
const MinJoinSecretLen = 8

// HashJoinSecret validates and hashes a team's join password. Every path that puts a secret
// on a team goes through here, so the length rule cannot be met on one route and skipped on
// the next.
func HashJoinSecret(secret string) (string, error) {
	if len(secret) < MinJoinSecretLen {
		return "", ErrJoinSecretTooShort
	}
	h, err := Hash(secret)
	if err != nil {
		return "", fmt.Errorf("accounts: join secret: %w", err)
	}
	return h, nil
}

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
	ID   int64
	Name string
	// Email is contact data for the team's own view; the public profile never carries it.
	Email       *string
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
	hash, err := HashJoinSecret(password)
	if err != nil {
		return Team{}, err
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

	// Every team holds a real secret — the column is NOT NULL — so there is no "this one has
	// no password" arm to fall through. A team locked by the migration that introduced the
	// requirement carries a hash nothing verifies against, and is refused here like any wrong
	// guess: saying which teams are locked would be the same enumeration oracle as saying
	// which names exist.
	ok, rehash := Verify(t.PasswordHash, password)
	if !ok {
		return Team{}, ErrTeamJoinDenied
	}
	if rehash {
		s.rehashTeamPassword(ctx, t.ID, password)
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
			TeamID: teamID, PasswordHash: upgraded,
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
//
// cutoff is the caller's freeze horizon (nil = live), and it clamps both the team score and the
// per-member breakdown. This page is a scoreboard row: served live during a freeze it hands over
// the post-freeze standings — and who scored what to earn them — one team id at a time.
func (s *Service) TeamProfile(ctx context.Context, teamID int64, cutoff *time.Time) (Team, error) {
	row, err := s.q.GetTeamPublicProfile(ctx, db.GetTeamPublicProfileParams{
		TeamID: teamID, Cutoff: cutoffArg(cutoff),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, fmt.Errorf("%w: id=%d", ErrTeamNotFound, teamID)
	} else if err != nil {
		return Team{}, fmt.Errorf("accounts: team profile: %w", err)
	}

	members, err := s.teamMembers(ctx, teamID, false, cutoff)
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
//
// No freeze cutoff, deliberately: an account always sees its own live score. It tells them nothing
// they could not count themselves, and a team that cannot see its own solves land reads as a bug.
func (s *Service) OwnTeam(ctx context.Context, userID int64) (Team, error) {
	row, err := s.q.GetOwnTeam(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, ErrNotOnTeam
	} else if err != nil {
		return Team{}, fmt.Errorf("accounts: own team: %w", err)
	}

	members, err := s.teamMembers(ctx, row.ID, true, nil)
	if err != nil {
		return Team{}, fmt.Errorf("accounts: own team: %w", err)
	}

	return Team{
		ID: row.ID, Name: row.Name, Email: row.Email,
		Website: row.Website, Affiliation: row.Affiliation, Country: row.Country,
		Score: row.Score, CreatedAt: row.CreatedAt.Time,
		IsCaptain: row.CaptainID != nil && *row.CaptainID == userID,
		Members:   members,
	}, nil
}

// TeamPatch is the captain-editable slice of the team: nil keeps, the Clear flags null.
type TeamPatch struct {
	Email       *string
	Website     *string
	Affiliation *string
	Country     *string

	ClearEmail       bool
	ClearWebsite     bool
	ClearAffiliation bool
	ClearCountry     bool
}

// UpdateOwnTeam lets the captain edit the team's contact and profile data. Captaincy is enforced
// in the UPDATE's WHERE clause, so a demoted captain's in-flight write affects zero rows rather
// than racing past a check.
func (s *Service) UpdateOwnTeam(ctx context.Context, userID int64, patch TeamPatch) (Team, error) {
	_, err := s.q.UpdateTeamByCaptain(ctx, db.UpdateTeamByCaptainParams{
		UserID: userID,
		Email:  patch.Email, Website: patch.Website,
		Affiliation: patch.Affiliation, Country: patch.Country,
		ClearEmail:       patch.ClearEmail,
		ClearWebsite:     patch.ClearWebsite,
		ClearAffiliation: patch.ClearAffiliation,
		ClearCountry:     patch.ClearCountry,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Not the captain — or not on a team at all. Tell those two apart for the caller.
		u, uerr := s.q.GetUserByID(ctx, userID)
		if uerr != nil {
			return Team{}, fmt.Errorf("accounts: update team: %w", uerr)
		}
		if u.TeamID == nil {
			return Team{}, ErrNotOnTeam
		}
		return Team{}, ErrNotCaptain
	case err != nil:
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "teams_email_uniq" {
			return Team{}, ErrTeamEmailTaken
		}
		return Team{}, fmt.Errorf("accounts: update team: %w", err)
	}
	return s.OwnTeam(ctx, userID)
}

// cutoff is the freeze horizon: a non-nil value hides solves at or after it. nil means live.
func cutoffArg(cutoff *time.Time) pgtype.Timestamptz {
	if cutoff == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *cutoff, Valid: true}
}

func (s *Service) teamMembers(ctx context.Context, teamID int64, includeMasked bool, cutoff *time.Time) ([]TeamMember, error) {
	rows, err := s.q.ListTeamMembers(ctx, db.ListTeamMembersParams{
		TeamID: &teamID, IncludeMasked: includeMasked, Cutoff: cutoffArg(cutoff),
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

// captainTx runs a roster mutation inside a transaction that stamps the acting captain, so the
// audit triggers on the mutated users/teams rows record who performed the change.
func (s *Service) captainTx(ctx context.Context, actor audit.Actor, fn func(q *db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("accounts: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := audit.Stamp(ctx, tx, actor); err != nil {
		return fmt.Errorf("accounts: %w", err)
	}
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("accounts: commit: %w", err)
	}
	return nil
}

// SetJoinSecret rotates the team's join password. Captaincy is the UPDATE's WHERE clause, so a
// captain demoted mid-flight changes nothing rather than racing past a check.
//
// It is also the only exit for a team whose row predates the rule that every team holds a secret:
// those carry a hash nothing verifies against and refuse every join until this runs.
func (s *Service) SetJoinSecret(ctx context.Context, actor audit.Actor, secret string) error {
	hash, err := HashJoinSecret(secret)
	if err != nil {
		return err
	}

	var rows int64
	err = s.captainTx(ctx, actor, func(q *db.Queries) error {
		var qerr error
		rows, qerr = q.SetJoinSecretByCaptain(ctx, db.SetJoinSecretByCaptainParams{
			PasswordHash: hash, CaptainID: &actor.ID,
		})
		if qerr != nil {
			return fmt.Errorf("accounts: set join secret: %w", qerr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		u, uerr := s.q.GetUserByID(ctx, actor.ID)
		if uerr != nil {
			return fmt.Errorf("accounts: set join secret: %w", uerr)
		}
		if u.TeamID == nil {
			return ErrNotOnTeam
		}
		return ErrNotCaptain
	}
	return nil
}

// KickMember removes a teammate the captain names. Captaincy, the target's membership, and the
// scored guard all live in the single UPDATE, so a demoted captain, a stale target, or a team
// already on the board changes nothing. A zero-row result is re-read only to tell the caller which
// rule refused it — that read never authorises anything, the statement already did.
func (s *Service) KickMember(ctx context.Context, actor audit.Actor, memberID int64) (Team, error) {
	var rows int64
	err := s.captainTx(ctx, actor, func(q *db.Queries) error {
		var qerr error
		rows, qerr = q.KickMember(ctx, db.KickMemberParams{MemberID: memberID, CaptainID: actor.ID})
		if qerr != nil {
			return fmt.Errorf("accounts: kick member: %w", qerr)
		}
		return nil
	})
	if err != nil {
		return Team{}, err
	}
	if rows == 0 {
		if memberID == actor.ID {
			return Team{}, ErrCannotKickSelf
		}
		return Team{}, s.rosterRefusal(ctx, actor.ID, memberID, true)
	}
	return s.OwnTeam(ctx, actor.ID)
}

// TransferCaptaincy hands the seat to another current member. Both the caller's captaincy and the
// target's membership are the UPDATE's WHERE, so nothing moves unless both hold at commit time.
func (s *Service) TransferCaptaincy(ctx context.Context, actor audit.Actor, newCaptainID int64) (Team, error) {
	captainID := actor.ID
	var rows int64
	err := s.captainTx(ctx, actor, func(q *db.Queries) error {
		var qerr error
		rows, qerr = q.TransferCaptaincy(ctx, db.TransferCaptaincyParams{
			NewCaptainID: &newCaptainID, CaptainID: &captainID,
		})
		if qerr != nil {
			return fmt.Errorf("accounts: transfer captaincy: %w", qerr)
		}
		return nil
	})
	if err != nil {
		return Team{}, err
	}
	if rows == 0 {
		return Team{}, s.rosterRefusal(ctx, captainID, newCaptainID, false)
	}
	return s.OwnTeam(ctx, captainID)
}

// DisbandTeam deletes the captain's team. The ledger's RESTRICT foreign keys are the guard: a team
// with any submission, solve, award, or hint unlock refuses the delete (23503), which becomes
// ErrTeamHasHistory. A zero-history team is deleted, its members freed by the ON DELETE SET NULL.
func (s *Service) DisbandTeam(ctx context.Context, actor audit.Actor) error {
	var rows int64
	err := s.captainTx(ctx, actor, func(q *db.Queries) error {
		var qerr error
		rows, qerr = q.DisbandTeam(ctx, actor.ID)
		if qerr != nil {
			var pg *pgconn.PgError
			if errors.As(qerr, &pg) && pg.Code == "23503" {
				return ErrTeamHasHistory
			}
			return fmt.Errorf("accounts: disband team: %w", qerr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		caller, cerr := s.q.GetUserByID(ctx, actor.ID)
		if cerr != nil {
			return fmt.Errorf("accounts: disband team: %w", cerr)
		}
		if caller.TeamID == nil {
			return ErrNotOnTeam
		}
		return ErrNotCaptain
	}
	return nil
}

// rosterRefusal names the rule that turned a zero-row kick or transfer away, for a precise error.
// scoredBlocks reflects whether the caller's statement carried the scored guard (kick does,
// transfer does not), so a scored team is only reported as such where it was actually the cause.
func (s *Service) rosterRefusal(ctx context.Context, captainID, targetID int64, scoredBlocks bool) error {
	caller, err := s.q.GetUserByID(ctx, captainID)
	if err != nil {
		return fmt.Errorf("accounts: roster refusal: caller: %w", err)
	}
	if caller.TeamID == nil {
		return ErrNotOnTeam
	}
	info, err := s.q.TeamCaptainScored(ctx, *caller.TeamID)
	if err != nil {
		return fmt.Errorf("accounts: roster refusal: team: %w", err)
	}
	if info.CaptainID == nil || *info.CaptainID != captainID {
		return ErrNotCaptain
	}
	target, err := s.q.GetUserByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTargetNotMember
		}
		return fmt.Errorf("accounts: roster refusal: target: %w", err)
	}
	if target.TeamID == nil || *target.TeamID != *caller.TeamID {
		return ErrTargetNotMember
	}
	if scoredBlocks && info.Scored {
		return ErrTeamHasScored
	}
	return ErrTargetNotMember
}
