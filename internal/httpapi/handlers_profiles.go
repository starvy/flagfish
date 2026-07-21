package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/domain/policy"
)

type userIDInput struct {
	ID int64 `path:"id"`
}

type profileSolveBody struct {
	ChallengeID   int64     `json:"challenge_id"`
	ChallengeName string    `json:"challenge_name"`
	Category      string    `json:"category"`
	Value         int32     `json:"value"`
	Date          time.Time `json:"date"`
}

// userProfileBody is a user's public page. It carries no contact address and no moderation state:
// the query that fills it never selects them.
type userProfileBody struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Website     *string `json:"website,omitempty"`
	Affiliation *string `json:"affiliation,omitempty"`
	Country     *string `json:"country,omitempty"`
	BracketID   *int64  `json:"bracket_id,omitempty"`
	BracketName *string `json:"bracket_name,omitempty"`
	// Score is null — not absent, not 0 — when scores are hidden from this viewer. The account
	// still resolves (name, affiliation); only the figure is withheld.
	Score     *int64             `json:"score"`
	CreatedAt time.Time          `json:"created_at"`
	Solves    []profileSolveBody `json:"solves"`
	// Points is the user's OWN cumulative score curve, keyed on their stamped ledger — in teams mode
	// their personal share, never the team's. It rides score_visibility exactly as Score and Solves do
	// and comes back empty when scores are withheld, so the chart shares the page's one visibility gate.
	Points []scorePointBody `json:"points"`
}

type userProfileOutput struct {
	Body userProfileBody
}

func (s *Server) registerProfiles() {
	Register(s.Public, policy.ClassAccountDetail, huma.Operation{
		OperationID: "user-detail", Method: http.MethodGet, Path: "/users/{id}",
		Summary: "A user's public profile", Tags: []string{"accounts"},
	}, s.userDetail)
}

// userDetail is a public scoreboard row with a solve history attached, so it clamps to the freeze
// exactly as the board and the team page do. The admin flag only widens the row to a hidden or banned
// account — it is the sole difference between a 404 and a 200 for one of those.
func (s *Server) userDetail(ctx context.Context, in *userIDInput) (*userProfileOutput, error) {
	pol := PolicyOf(ctx)
	admin := AuthOf(ctx).Principal.IsAdmin
	p, err := s.opts.Accounts.UserProfile(ctx, in.ID, admin, freezeCutoff(pol))
	switch {
	case errors.Is(err, accounts.ErrUserNotFound):
		return nil, huma.Error404NotFound("user not found")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "user profile failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load the profile")
	}

	// score_visibility governs the figures, not the existence of the page — the account-visibility
	// gate above already decided the latter. So the score and the solve history are redacted here
	// rather than hidden by a route gate, which would turn "scores are hidden" into a 404.
	red := policy.NewRedactor(pol)

	score := p.Score
	af := policy.AccountFields{Score: &score}
	red.Account(&af)

	entries := make([]policy.ProfileSolveEntry, len(p.Solves))
	for i, sv := range p.Solves {
		entries[i] = policy.ProfileSolveEntry{
			ChallengeID: sv.ChallengeID, ChallengeName: sv.ChallengeName,
			Category: sv.Category, Value: sv.Value, Date: sv.Date,
		}
	}
	shown := red.ProfileSolveList(entries)
	solves := make([]profileSolveBody, 0, len(shown))
	for _, e := range shown {
		solves = append(solves, profileSolveBody{
			ChallengeID: e.ChallengeID, ChallengeName: e.ChallengeName,
			Category: e.Category, Value: e.Value, Date: e.Date,
		})
	}

	curve := make([]policy.HistoryPoint, len(p.History))
	for i, pt := range p.History {
		curve[i] = policy.HistoryPoint{Date: pt.Date, Delta: pt.Delta, Score: pt.Score}
	}
	shownCurve := red.ProfileScoreHistory(curve)
	points := make([]scorePointBody, 0, len(shownCurve))
	for _, pt := range shownCurve {
		points = append(points, scorePointBody{Date: pt.Date, Delta: pt.Delta, Score: pt.Score})
	}

	out := &userProfileOutput{Body: userProfileBody{
		ID: p.ID, Name: p.Name,
		Website: p.Website, Affiliation: p.Affiliation, Country: p.Country,
		BracketID: p.BracketID, BracketName: p.BracketName,
		Score: af.Score, CreatedAt: p.CreatedAt, Solves: solves, Points: points,
	}}
	return out, nil
}
