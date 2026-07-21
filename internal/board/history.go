package board

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/starvy/flagfish/internal/db"
)

// HistoryPoint is one instant on an account's cumulative score curve: Delta is the ledger event's
// own value, Score the running total up to and including it. A graph draws a step per point.
type HistoryPoint struct {
	Date  time.Time
	Delta int64
	Score int64
}

// ScoreHistory replays one account's score ledger into a cumulative timeline. admin only widens the
// result to a hidden or banned subject — it never lifts the freeze. cutoff is the horizon every event
// is clamped behind (nil for a live view); the caller folds ?as_of into it exactly as the scoreboard
// does, so this read is frozen by the same mechanism rather than a second one that could drift.
func (s *Service) ScoreHistory(ctx context.Context, accountID int64, admin bool, cutoff *time.Time) ([]HistoryPoint, error) {
	var cut pgtype.Timestamptz
	if cutoff != nil {
		cut = pgtype.Timestamptz{Time: *cutoff, Valid: true}
	}
	rows, err := s.q.GetScoreHistory(ctx, db.GetScoreHistoryParams{
		AccountID: accountID,
		Admin:     admin,
		Cutoff:    cut,
	})
	if err != nil {
		return nil, fmt.Errorf("board: score history for account %d: %w", accountID, err)
	}
	out := make([]HistoryPoint, len(rows))
	for i, r := range rows {
		out[i] = HistoryPoint{Date: r.Date.Time, Delta: r.Delta, Score: r.Cumulative}
	}
	return out, nil
}
