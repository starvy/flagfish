// Package board is the read side of the scoreboard. It reads the append-only score ledger; the mode
// decides which account column the standings key on, and it is fixed at setup, never per request.
package board

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
)

var Module = fx.Module("board", fx.Provide(New))

type Service struct {
	q    *db.Queries
	mode account.Mode
}

func New(pool *pgxpool.Pool, mode account.Mode) *Service {
	return &Service{q: db.New(pool), mode: mode}
}

// clampLimit maps a caller's row limit onto the query's int32; negative or absurd means "no limit" (0).
func clampLimit(n int) int32 {
	if n <= 0 || n > 1_000_000 {
		return 0
	}
	return int32(n)
}

// Entry is one row of standings; rank is positional, so the first entry is rank 1.
type Entry struct {
	AccountID   int64
	Name        string
	BracketID   *int64
	BracketName *string
	Score       int64
	LastEvent   time.Time
}

// Top returns the live standings, highest first. admin includes hidden and banned accounts; a
// non-nil bracketID narrows the board to one division; limit 0 means no limit. The bracket is a
// filter over the one ranking, not a separate pool, so ranks stay relative to whoever is shown.
func (s *Service) Top(ctx context.Context, admin bool, bracketID *int64, limit int) ([]Entry, error) {
	switch s.mode {
	case account.ModeTeams:
		rows, err := s.q.GetTeamStandings(ctx, db.GetTeamStandingsParams{Admin: admin, BracketID: bracketID, Lim: clampLimit(limit)})
		if err != nil {
			return nil, fmt.Errorf("board: team standings: %w", err)
		}
		out := make([]Entry, len(rows))
		for i, r := range rows {
			out[i] = Entry{
				AccountID: r.AccountID, Name: r.Name, BracketID: r.BracketID,
				BracketName: r.BracketName, Score: r.Score, LastEvent: asTime(r.LastEvent),
			}
		}
		return out, nil
	default:
		rows, err := s.q.GetUserStandings(ctx, db.GetUserStandingsParams{Admin: admin, BracketID: bracketID, Lim: clampLimit(limit)})
		if err != nil {
			return nil, fmt.Errorf("board: user standings: %w", err)
		}
		out := make([]Entry, len(rows))
		for i, r := range rows {
			out[i] = Entry{
				AccountID: r.AccountID, Name: r.Name, BracketID: r.BracketID,
				BracketName: r.BracketName, Score: r.Score, LastEvent: asTime(r.LastEvent),
			}
		}
		return out, nil
	}
}

// AsOf returns the standings as of asOf, optionally narrowed to one bracket. Callers clamp asOf to
// the freeze horizon for non-admin viewers; the bracket filter composes with that clamp — it
// narrows the rows the frozen board returns, it does not lift the freeze.
func (s *Service) AsOf(ctx context.Context, asOf time.Time, admin bool, bracketID *int64, limit int) ([]Entry, error) {
	rows, err := s.q.GetStandingsAsOf(ctx, db.GetStandingsAsOfParams{
		AsOf:      pgtype.Timestamptz{Time: asOf, Valid: true},
		Admin:     admin,
		BracketID: bracketID,
		Lim:       clampLimit(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("board: standings as of %s: %w", asOf, err)
	}
	out := make([]Entry, len(rows))
	for i, r := range rows {
		out[i] = Entry{AccountID: r.AccountID, Name: r.Name, BracketID: r.BracketID, Score: r.Score, LastEvent: asTime(r.LastEvent)}
	}
	return out, nil
}

// Bracket is a division a client can filter the board by.
type Bracket struct {
	ID          int64
	Name        string
	Description *string
}

// Brackets lists the divisions that apply to this instance's account kind, so a client can offer
// the ?bracket filter. Brackets of the other kind can never hold a member here, so they are hidden.
func (s *Service) Brackets(ctx context.Context) ([]Bracket, error) {
	kind := s.mode.String()
	rows, err := s.q.ListBrackets(ctx, &kind)
	if err != nil {
		return nil, fmt.Errorf("board: list brackets: %w", err)
	}
	out := make([]Bracket, len(rows))
	for i, r := range rows {
		out[i] = Bracket{ID: r.ID, Name: r.Name, Description: r.Description}
	}
	return out, nil
}

func asTime(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}
