package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/domain/policy"
)

type challengeIDInput struct {
	ID int64 `path:"id"`
}

type challengeListItem struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Value      int32  `json:"value"`
	Function   string `json:"function"`
	SolveCount *int64 `json:"solve_count"`
	Solved     bool   `json:"solved"`
	Locked     bool   `json:"locked"`
}

type challengesOutput struct {
	Body struct {
		Challenges []challengeListItem `json:"challenges"`
	}
}

type challengeFile struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

type challengeHint struct {
	ID       int64   `json:"id"`
	Title    *string `json:"title"`
	Cost     int32   `json:"cost"`
	Unlocked bool    `json:"unlocked"`
	Locked   bool    `json:"locked"`
}

type challengeDetailOutput struct {
	Body struct {
		ID             int64           `json:"id"`
		Name           string          `json:"name"`
		Category       string          `json:"category"`
		Description    string          `json:"description"`
		Attribution    *string         `json:"attribution,omitempty"`
		ConnectionInfo *string         `json:"connection_info,omitempty"`
		Type           string          `json:"type"`
		Value          int32           `json:"value"`
		Function       string          `json:"function"`
		MaxAttempts    int32           `json:"max_attempts"`
		State          string          `json:"state"`
		SolveCount     *int64          `json:"solve_count"`
		Solved         bool            `json:"solved"`
		Locked         bool            `json:"locked"`
		Tags           []string        `json:"tags"`
		Files          []challengeFile `json:"files"`
		Hints          []challengeHint `json:"hints"`
	}
}

type challengeSolve struct {
	Name  string    `json:"name"`
	Value int32     `json:"value"`
	Date  time.Time `json:"date"`
}

type solvesOutput struct {
	Body struct {
		Solves []challengeSolve `json:"solves"`
	}
}

func (s *Server) registerChallenges() {
	Register(s.Public, policy.ClassChallengeList, huma.Operation{
		OperationID: "list-challenges", Method: http.MethodGet, Path: "/challenges",
		Summary: "List the challenge board", Tags: []string{"challenges"},
	}, s.listChallenges)

	Register(s.Public, policy.ClassChallengeDetail, huma.Operation{
		OperationID: "challenge-detail", Method: http.MethodGet, Path: "/challenges/{id}",
		Summary: "Get one challenge with its tags, files and hints", Tags: []string{"challenges"},
	}, s.challengeDetail)

	Register(s.Public, policy.ClassChallengeSolves, huma.Operation{
		OperationID: "challenge-solves", Method: http.MethodGet, Path: "/challenges/{id}/solves",
		Summary: "List who solved a challenge", Tags: []string{"challenges"},
	}, s.challengeSolves)
}

// redactSolveCount nulls a challenge's solve count unless the caller may see both scores and accounts.
// A redacted count is null on the wire, never 0.
func redactSolveCount(red policy.Redactor, count int64) *int64 {
	c := int(count)
	if red.ChallengeSolveCount(&c) == nil {
		return nil
	}
	return &count
}

func (s *Server) listChallenges(ctx context.Context, _ *struct{}) (*challengesOutput, error) {
	a := s.actor(ctx)
	p := PolicyOf(ctx)
	rows, err := s.opts.Catalog.List(ctx, a.UserID, a.TeamID, freezeCutoff(p))
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "list challenges failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load challenges")
	}
	red := policy.NewRedactor(p)
	out := &challengesOutput{}
	out.Body.Challenges = make([]challengeListItem, len(rows))
	for i, r := range rows {
		out.Body.Challenges[i] = challengeListItem{
			ID: r.ID, Name: r.Name, Category: r.Category, Value: r.Value,
			Function: r.Function, SolveCount: redactSolveCount(red, r.SolveCount), Solved: r.Solved,
			Locked: r.Locked,
		}
	}
	return out, nil
}

func (s *Server) challengeDetail(ctx context.Context, in *challengeIDInput) (*challengeDetailOutput, error) {
	a := s.actor(ctx)
	p := PolicyOf(ctx)
	d, err := s.opts.Catalog.Detail(ctx, in.ID, a.UserID, a.TeamID, freezeCutoff(p))
	switch {
	case errors.Is(err, catalog.ErrChallengeNotFound):
		return nil, huma.Error404NotFound("challenge not found")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "challenge detail failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load challenge")
	}

	c := d.Challenge
	out := &challengeDetailOutput{}
	out.Body.ID = c.ID
	out.Body.Name = c.Name
	out.Body.Category = c.Category
	out.Body.Description = c.Description
	out.Body.Attribution = c.Attribution
	out.Body.ConnectionInfo = c.ConnectionInfo
	out.Body.Type = c.Type
	out.Body.Value = c.Value
	out.Body.Function = c.Function
	out.Body.MaxAttempts = c.MaxAttempts
	out.Body.State = c.State
	out.Body.SolveCount = redactSolveCount(policy.NewRedactor(p), c.SolveCount)
	out.Body.Solved = c.Solved
	out.Body.Locked = c.Locked

	out.Body.Tags = d.Tags
	if out.Body.Tags == nil {
		out.Body.Tags = []string{}
	}
	out.Body.Files = make([]challengeFile, len(d.Files))
	for i, f := range d.Files {
		out.Body.Files[i] = challengeFile{ID: f.ID, Name: f.Name, SizeBytes: f.SizeBytes}
	}
	out.Body.Hints = make([]challengeHint, len(d.Hints))
	for i, h := range d.Hints {
		out.Body.Hints[i] = challengeHint{ID: h.ID, Title: h.Title, Cost: h.Cost, Unlocked: h.Unlocked, Locked: h.Locked}
	}
	return out, nil
}

func (s *Server) challengeSolves(ctx context.Context, in *challengeIDInput) (*solvesOutput, error) {
	p := PolicyOf(ctx)
	rows, err := s.opts.Catalog.Solves(ctx, in.ID, freezeCutoff(p))
	switch {
	case errors.Is(err, catalog.ErrChallengeNotFound):
		return nil, huma.Error404NotFound("challenge not found")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "challenge solves failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load solves")
	}
	out := &solvesOutput{}
	out.Body.Solves = make([]challengeSolve, len(rows))
	for i, r := range rows {
		out.Body.Solves[i] = challengeSolve{Name: r.Name, Value: r.Value, Date: r.Date}
	}
	return out, nil
}
