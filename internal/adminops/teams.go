package adminops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
)

// TeamPage is one page of the team list plus the total the pagination is computed from.
type TeamPage struct {
	Teams []db.AdminListTeamsRow
	Total int64
}

// ListTeams pages through the teams, optionally narrowed by one (q, field) search pair.
// An empty q means no filter; an empty field searches the name.
func (s *Service) ListTeams(ctx context.Context, page, perPage int, q, field string) (TeamPage, error) {
	rows, err := s.q.AdminListTeams(ctx, db.AdminListTeamsParams{
		Q:     nilIfEmpty(q),
		Field: nilIfEmpty(field),
		Lim:   int32(perPage),              //nolint:gosec // Huma caps per_page at 100
		Off:   int32((page - 1) * perPage), //nolint:gosec // page is bounded by the handler; an over-large offset just returns an empty page
	})
	if err != nil {
		return TeamPage{}, fmt.Errorf("adminops: list teams: %w", err)
	}
	p := TeamPage{Teams: rows}
	if len(rows) > 0 {
		p.Total = rows[0].Total
	}
	return p, nil
}

func (s *Service) GetTeam(ctx context.Context, teamID int64) (db.AdminGetTeamRow, error) {
	row, err := s.q.AdminGetTeam(ctx, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AdminGetTeamRow{}, fmt.Errorf("%w: id=%d", ErrTeamNotFound, teamID)
	} else if err != nil {
		return db.AdminGetTeamRow{}, fmt.Errorf("adminops: get team %d: %w", teamID, err)
	}
	return row, nil
}

// NewTeam is the admin create input. PasswordHash is the already-hashed join password, or nil
// for a team that admits members on an empty password — hashing stays out of this package.
type NewTeam struct {
	Name         string
	PasswordHash *string
	Email        *string
	Website      *string
	Affiliation  *string
	Country      *string
}

func (s *Service) CreateTeam(ctx context.Context, actor audit.Actor, in NewTeam) (db.AdminCreateTeamRow, error) {
	var out db.AdminCreateTeamRow
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminCreateTeam(ctx, db.AdminCreateTeamParams{
			Name: in.Name, PasswordHash: in.PasswordHash, Email: in.Email,
			Website: in.Website, Affiliation: in.Affiliation, Country: in.Country,
		})
		if err != nil {
			return fmt.Errorf("adminops: create team: %w", teamConstraint(err))
		}
		return nil
	})
	return out, err
}

// TeamPatch is a partial update. nil means keep; the Clear flags null their column, which is
// distinct from leaving the value nil.
type TeamPatch struct {
	Name        *string
	Email       *string
	Website     *string
	Affiliation *string
	Country     *string

	ClearEmail       bool
	ClearWebsite     bool
	ClearAffiliation bool
	ClearCountry     bool
}

func (s *Service) UpdateTeam(ctx context.Context, actor audit.Actor, teamID int64, patch TeamPatch) (db.AdminUpdateTeamRow, error) {
	var out db.AdminUpdateTeamRow
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminUpdateTeam(ctx, db.AdminUpdateTeamParams{
			TeamID: teamID,
			Name:   patch.Name, Email: patch.Email,
			Website: patch.Website, Affiliation: patch.Affiliation, Country: patch.Country,
			ClearEmail:       patch.ClearEmail,
			ClearWebsite:     patch.ClearWebsite,
			ClearAffiliation: patch.ClearAffiliation,
			ClearCountry:     patch.ClearCountry,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrTeamNotFound, teamID)
		} else if err != nil {
			return fmt.Errorf("adminops: update team %d: %w", teamID, teamConstraint(err))
		}
		return nil
	})
	return out, err
}

// teamConstraint translates the two unique indexes and the caps trigger into named refusals.
func teamConstraint(err error) error {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return err
	}
	switch {
	case pg.Code == "23505" && pg.ConstraintName == "teams_email_uniq":
		return ErrTeamEmailTaken
	case pg.Code == "23505":
		return ErrTeamNameTaken
	case pg.Code == "23514":
		return ErrTeamCapReached
	}
	return err
}

// SetTeamBanned bans or unbans a team, and a ban kills every member's live session in the same
// transaction. Banning the acting admin's own team is refused.
//
// A team ban locks its members out of every route just as an account ban does, so it is one of the
// ways the instance can lose its last admin, and refusing self-ban only closes the case where the
// caller is the admin in question. It takes the same lock and counts the same way a demotion does:
// banning a team that holds the only other usable admin is refused outright.
func (s *Service) SetTeamBanned(ctx context.Context, actor audit.Actor, teamID int64, banned bool) (db.AdminSetTeamBannedRow, error) {
	var out db.AdminSetTeamBannedRow
	err := s.tx(ctx, actor, func(tx pgx.Tx, q *db.Queries) error {
		if banned {
			if err := lockAdminRoster(ctx, tx); err != nil {
				return fmt.Errorf("adminops: ban team %d: %w", teamID, err)
			}
			u, err := q.GetUserByID(ctx, actor.ID)
			if err != nil {
				return fmt.Errorf("adminops: ban team %d: load actor: %w", teamID, err)
			}
			if u.TeamID != nil && *u.TeamID == teamID {
				return fmt.Errorf("%w: id=%d", ErrSelfTeamBan, teamID)
			}
			remaining, err := q.AdminCountAdminsOutsideTeam(ctx, &teamID)
			if err != nil {
				return fmt.Errorf("adminops: ban team %d: count admins: %w", teamID, err)
			}
			if remaining == 0 {
				return fmt.Errorf("%w: team_id=%d", ErrLastAdmin, teamID)
			}
		}
		var err error
		out, err = q.AdminSetTeamBanned(ctx, db.AdminSetTeamBannedParams{TeamID: teamID, Banned: banned})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrTeamNotFound, teamID)
		} else if err != nil {
			return fmt.Errorf("adminops: set team %d banned: %w", teamID, err)
		}
		if banned {
			if err := q.DeleteTeamSessions(ctx, &teamID); err != nil {
				return fmt.Errorf("adminops: ban team %d: kill sessions: %w", teamID, err)
			}
		}
		return nil
	})
	return out, err
}

func (s *Service) SetTeamHidden(ctx context.Context, actor audit.Actor, teamID int64, hidden bool) (db.AdminSetTeamHiddenRow, error) {
	var out db.AdminSetTeamHiddenRow
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminSetTeamHidden(ctx, db.AdminSetTeamHiddenParams{TeamID: teamID, Hidden: hidden})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrTeamNotFound, teamID)
		} else if err != nil {
			return fmt.Errorf("adminops: set team %d hidden: %w", teamID, err)
		}
		return nil
	})
	return out, err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
