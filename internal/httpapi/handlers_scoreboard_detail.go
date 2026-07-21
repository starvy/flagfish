package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
)

type scoreHistoryInput struct {
	ID int64 `path:"id"`
	// AsOf replays the curve up to a past instant (RFC3339). Like the scoreboard it is clamped to the
	// freeze horizon for a viewer the freeze still applies to, so ?as_of during a freeze can never draw
	// the post-freeze curve. A freeze-exempt viewer (the admin surface) is honoured as asked.
	AsOf time.Time `query:"as_of"`
}

type scorePointBody struct {
	Date  time.Time `json:"date"`
	Delta int64     `json:"delta"`
	Score int64     `json:"score"`
}

type scoreHistoryOutput struct {
	Body struct {
		AccountID int64            `json:"account_id"`
		Points    []scorePointBody `json:"points"`
	}
}

func (s *Server) registerScoreboardDetail() {
	Register(s.Public, policy.ClassScoreboardDetail, huma.Operation{
		OperationID: "scoreboard-detail", Method: http.MethodGet, Path: "/scoreboard/{id}",
		Summary: "One account's cumulative score over time (freeze-safe; honours ?as_of)",
		Tags:    []string{"scoreboard"},
	}, s.scoreboardDetail)
}

// scoreboardDetail draws one account's score curve. It clamps to the freeze exactly as the main board
// does: the cutoff is the one freeze decision, and ?as_of can only ever move it earlier for a
// non-exempt viewer, never past the horizon. admin here only widens the curve to a hidden or banned
// subject — the freeze is decided by policy, not by the role.
func (s *Server) scoreboardDetail(ctx context.Context, in *scoreHistoryInput) (*scoreHistoryOutput, error) {
	p := PolicyOf(ctx)
	admin := AuthOf(ctx).Principal.IsAdmin

	cutoff := freezeCutoff(p)
	if !in.AsOf.IsZero() {
		asOf := in.AsOf
		if policy.Frozen(p) && asOf.After(*p.E.FreezeAt) {
			asOf = *p.E.FreezeAt
		}
		cutoff = &asOf
	}

	points, err := s.opts.Board.ScoreHistory(ctx, in.ID, admin, cutoff)
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "score history read failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load the score history")
	}

	out := &scoreHistoryOutput{}
	out.Body.AccountID = in.ID
	out.Body.Points = make([]scorePointBody, len(points))
	for i, pt := range points {
		out.Body.Points[i] = scorePointBody{Date: pt.Date, Delta: pt.Delta, Score: pt.Score}
	}
	return out, nil
}
