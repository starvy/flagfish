package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/flags"
	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/jobs"
)

// JobEnqueuer enqueues a job outside any transaction. *jobs.Inserter (a *river.Client[pgx.Tx])
// satisfies it. The pool-exhaustion alert is enqueued this way: the issue transaction that hit the
// dry pool has already rolled back, so there is nothing to bind the enqueue to.
type JobEnqueuer interface {
	Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// errNoIssuer is a wiring bug, not a player-facing condition: the catalog is serving a unique-flag
// challenge with no gameplay service to assign from. Serving the challenge without an instance
// would hand out a flagless, unsolvable body and quietly void the uniqueness property, so it fails.
var errNoIssuer = errors.New("httpapi: unique-flag challenge but no gameplay service is wired")

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

// challengeInstance is the caller's own bundle for a unique-flag challenge: the per-account
// variables the description is written against, and the artifact the author generated for them.
// There is no flag field, here or anywhere below it — only the hash is ever stored.
type challengeInstance struct {
	InstanceID int64          `json:"instance_id"`
	ArtifactID *int64         `json:"artifact_id,omitempty"`
	Vars       map[string]any `json:"vars"`
}

type challengeDetailOutput struct {
	Body struct {
		ID             int64              `json:"id"`
		Name           string             `json:"name"`
		Category       string             `json:"category"`
		Description    string             `json:"description"`
		Attribution    *string            `json:"attribution,omitempty"`
		ConnectionInfo *string            `json:"connection_info,omitempty"`
		Type           string             `json:"type"`
		Value          int32              `json:"value"`
		Function       string             `json:"function"`
		MaxAttempts    int32              `json:"max_attempts"`
		State          string             `json:"state"`
		FlagMode       string             `json:"flag_mode"`
		SolveCount     *int64             `json:"solve_count"`
		Solved         bool               `json:"solved"`
		Locked         bool               `json:"locked"`
		NextID         *int64             `json:"next_id,omitempty"`
		Tags           []string           `json:"tags"`
		Files          []challengeFile    `json:"files"`
		Hints          []challengeHint    `json:"hints"`
		Instance       *challengeInstance `json:"instance,omitempty"`
	}
}

type challengeSolve struct {
	Name  string    `json:"name"`
	Value int32     `json:"value"`
	Date  time.Time `json:"date"`
}

type challengeSolvesInput struct {
	ID int64 `path:"id"`
	// Cursor is the opaque keyset position from the previous page's next_cursor. Absent means the
	// first page. Deliberately not an offset — solves land while a reader scrolls.
	Cursor string `query:"cursor"`
	// A tighter cap than the admin feed's: this is an unprivileged route on the public surface, and
	// the whole point of the bound is that a popular challenge cannot be scraped in one request.
	Limit int `query:"limit" minimum:"1" maximum:"100" default:"50"`
}

type solvesOutput struct {
	Body struct {
		Solves []challengeSolve `json:"solves"`
		// NextCursor is empty on the last page. Feed it back as ?cursor to fetch the next.
		NextCursor string `json:"next_cursor,omitempty"`
		Limit      int    `json:"limit"`
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
	inst, err := s.issueForView(ctx, &c)
	switch {
	case errors.Is(err, flags.ErrPoolExhausted):
		// Loud, and never a challenge body with no flag in it: a silent fallback would destroy
		// uniqueness for exactly the late registrants the detector exists to catch. The alert is
		// deduplicated per challenge downstream, so a stampede of late registrants is one notice.
		s.opts.Log.ErrorContext(ctx, "challenge instance pool exhausted",
			"challenge_id", c.ID, "challenge", c.Name)
		s.enqueuePoolAlert(ctx, c.ID, c.Name)
		return nil, huma.Error503ServiceUnavailable("this challenge has no instances left — tell an organiser")
	case errors.Is(err, account.ErrTeamless):
		return nil, huma.Error403Forbidden("join a team to play")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "issue challenge instance failed", "challenge_id", c.ID, "error", err)
		return nil, huma.Error500InternalServerError("could not load challenge")
	}

	out := &challengeDetailOutput{}
	out.Body.Instance = inst
	out.Body.FlagMode = c.FlagMode.String()
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
	out.Body.NextID = c.NextID

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

// issueForView returns the caller's instance for a unique-flag challenge, assigning one from the
// pool on their first view. It is the only read in this product that writes, so the gate is narrow
// and everything outside it costs nothing: a static challenge, an anonymous viewer, an admin, and a
// challenge whose prerequisites are unmet all return here without touching the database.
//
// Admins are excluded because a preview that burned an instance would take a flag from a player who
// needs one, and because the pool is sized for the field, not for the organisers. Hidden accounts
// are NOT excluded: hiddenness keeps an account off the board, it does not stop it playing, and an
// account that can solve must be able to hold its own flag — otherwise its solves are
// indistinguishable from a shared one to the unissued-solve detector.
//
// The write happens once per (challenge, account): every later view finds the existing row and
// returns it. Idempotence is the primary key's, not this function's.
func (s *Server) issueForView(ctx context.Context, ch *catalog.Challenge) (*challengeInstance, error) {
	pr := AuthOf(ctx).Principal
	if ch.FlagMode != flags.ModeUnique || ch.Locked || !pr.Authed || pr.IsAdmin {
		return nil, nil
	}
	if s.opts.Gameplay == nil {
		return nil, errNoIssuer
	}
	acct, err := s.actor(ctx).AccountID(s.opts.Config.Current().Mode)
	if err != nil {
		return nil, fmt.Errorf("httpapi: issue for view: %w", err)
	}
	issued, err := s.opts.Gameplay.IssueInstance(ctx, ch.ID, int64(acct))
	if err != nil {
		return nil, fmt.Errorf("httpapi: issue for view: challenge %d: %w", ch.ID, err)
	}
	return instanceBody(issued)
}

// enqueuePoolAlert raises the deduplicated admin alert for a dry pool. It is best-effort: a failed
// enqueue must never turn the player's 503 into a 500, so it is logged and swallowed. The nil-guard
// covers the wiring (tests, doc generation) that runs without a job client.
func (s *Server) enqueuePoolAlert(ctx context.Context, challengeID int64, name string) {
	if s.opts.Jobs == nil {
		return
	}
	if _, err := s.opts.Jobs.Insert(ctx, jobs.PoolExhaustedAlert{
		ChallengeID: challengeID, ChallengeName: name,
	}, nil); err != nil {
		s.opts.Log.ErrorContext(ctx, "enqueue pool-exhausted alert failed", "challenge_id", challengeID, "error", err)
	}
}

func instanceBody(i gameplay.IssuedInstance) (*challengeInstance, error) {
	vars := map[string]any{}
	if len(i.Vars) > 0 {
		if err := json.Unmarshal(i.Vars, &vars); err != nil {
			return nil, fmt.Errorf("httpapi: instance %d: decode vars: %w", i.InstanceID, err)
		}
	}
	return &challengeInstance{InstanceID: i.InstanceID, ArtifactID: i.ArtifactID, Vars: vars}, nil
}

func (s *Server) challengeSolves(ctx context.Context, in *challengeSolvesInput) (*solvesOutput, error) {
	p := PolicyOf(ctx)

	var after *catalog.SolveCursor
	if in.Cursor != "" {
		date, id, err := decodeKeysetCursor(in.Cursor)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid cursor")
		}
		after = &catalog.SolveCursor{Date: date, ID: id}
	}

	page, err := s.opts.Catalog.Solves(ctx, in.ID, freezeCutoff(p), after, in.Limit)
	switch {
	case errors.Is(err, catalog.ErrChallengeNotFound):
		return nil, huma.Error404NotFound("challenge not found")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "challenge solves failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load solves")
	}

	entries := make([]policy.SolveEntry, len(page.Rows))
	for i, r := range page.Rows {
		entries[i] = policy.SolveEntry{Name: r.Name, Value: r.Value, Date: r.Date}
	}
	shown := policy.NewRedactor(p).ChallengeSolveList(entries)

	out := &solvesOutput{}
	out.Body.Limit = in.Limit
	out.Body.Solves = make([]challengeSolve, len(shown))
	for i, e := range shown {
		out.Body.Solves[i] = challengeSolve{Name: e.Name, Value: e.Value, Date: e.Date}
	}
	// nil means the list was withheld, and the cursor goes with it: a resume token for a list we
	// refused to serve is an invitation to page through it.
	if shown != nil && page.Next != nil {
		out.Body.NextCursor = encodeKeysetCursor(page.Next.Date, page.Next.ID)
	}
	return out, nil
}
