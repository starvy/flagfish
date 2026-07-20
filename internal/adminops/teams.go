package adminops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

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

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
