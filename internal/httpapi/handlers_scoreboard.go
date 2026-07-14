package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/domain/policy"
)

type scoreboardInput struct {
	// Bounded on both ends on purpose: board.clampLimit reads 0 as "no limit", so an absent
	// or zero limit would make the ordinary front-page request re-aggregate the whole ledger
	// and return every account — uncached, at whatever rate the limiter allows.
	Limit int `query:"limit" minimum:"1" maximum:"1000" default:"100"`
	// AsOf time-travels the standings to a past instant (RFC3339). It is a pure
	// function of the immutable score ledger. For a viewer the freeze still applies
	// to, it is clamped to the freeze horizon — a non-exempt caller can never travel
	// past it. A future instant yields the current standings; an instant at or before
	// the first scoring event yields an empty board.
	AsOf time.Time `query:"as_of"`
	// Preview lets a freeze-exempt viewer (an admin) see the live board during a
	// freeze. It is honoured by the policy layer; a non-exempt caller's preview is
	// ignored and never a bypass. Declared here so it appears in the API contract.
	Preview bool `query:"preview"`
	// Bracket narrows the standings to one division. It is a filter over the same ranking,
	// so it composes with the freeze and time-travel above rather than bypassing them. A
	// non-integer or non-positive value is rejected as 422 by the schema; absent (0) means the
	// overall board, and an id that matches no bracket simply yields an empty board.
	Bracket int64 `query:"bracket" minimum:"1"`
}

type standing struct {
	Rank      int    `json:"rank"`
	AccountID int64  `json:"account_id"`
	Name      string `json:"name"`
	Score     int64  `json:"score"`
}

type scoreboardOutput struct {
	Body struct {
		Standings []standing `json:"standings"`
	}
}

// bracketBody is one division a client can filter the scoreboard by.
type bracketBody struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type bracketsOutput struct {
	Body struct {
		Brackets []bracketBody `json:"brackets"`
	}
}

func (s *Server) registerScoreboard() {
	Register(s.Public, policy.ClassScoreboard, huma.Operation{
		OperationID: "scoreboard", Method: http.MethodGet, Path: "/scoreboard",
		Summary: "Get the standings", Tags: []string{"scoreboard"},
	}, s.scoreboard)

	// Gated exactly like the board it filters: the list is only useful to a viewer who can
	// see the scoreboard, and it exposes no per-account data.
	Register(s.Public, policy.ClassScoreboard, huma.Operation{
		OperationID: "brackets", Method: http.MethodGet, Path: "/brackets",
		Summary: "List scoreboard brackets", Tags: []string{"scoreboard"},
	}, s.brackets)
}

func (s *Server) brackets(ctx context.Context, _ *struct{}) (*bracketsOutput, error) {
	list, err := s.opts.Board.Brackets(ctx)
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "bracket list read failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load brackets")
	}
	out := &bracketsOutput{}
	out.Body.Brackets = make([]bracketBody, len(list))
	for i, b := range list {
		out.Body.Brackets[i] = bracketBody{ID: b.ID, Name: b.Name, Description: b.Description}
	}
	return out, nil
}

func (s *Server) scoreboard(ctx context.Context, in *scoreboardInput) (*scoreboardOutput, error) {
	p := PolicyOf(ctx)
	admin := p.P.IsAdmin

	// 0 (absent) means the overall board; any positive id narrows to that bracket.
	var bracket *int64
	if in.Bracket > 0 {
		bracket = &in.Bracket
	}

	var (
		entries []board.Entry
		err     error
	)
	// `admin` only widens the rows to hidden/banned accounts; the freeze is decided by policy.
	switch {
	case !in.AsOf.IsZero():
		// Time-travel to the requested instant. A viewer the freeze still applies to
		// can never travel past its horizon, so ?as_of during a freeze cannot hand a
		// non-exempt caller the post-freeze board. A freeze-exempt viewer (the admin
		// surface, or an admin who asked with ?preview) is honoured as requested.
		asOf := in.AsOf
		if policy.Frozen(p) && asOf.After(*p.E.FreezeAt) {
			asOf = *p.E.FreezeAt
		}
		entries, err = s.opts.Board.AsOf(ctx, asOf, admin, bracket, in.Limit)
	case policy.Frozen(p):
		// Default frozen board: the standings as they stood at the freeze.
		entries, err = s.opts.Board.AsOf(ctx, *p.E.FreezeAt, admin, bracket, in.Limit)
	default:
		entries, err = s.opts.Board.Top(ctx, admin, bracket, in.Limit)
	}
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "scoreboard read failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load the scoreboard")
	}

	out := &scoreboardOutput{}
	out.Body.Standings = make([]standing, len(entries))
	for i, e := range entries {
		out.Body.Standings[i] = standing{
			Rank:      i + 1,
			AccountID: e.AccountID,
			Name:      e.Name,
			Score:     e.Score,
		}
	}
	return out, nil
}
