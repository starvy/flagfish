// Package catalog is the read side of the challenge board: listing, detail, and the per-challenge
// solve list. It holds no locks and mutates nothing; the write path lives in gameplay.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/flags"
	"github.com/starvy/flagfish/internal/domain/prereq"
)

var ErrChallengeNotFound = errors.New("catalog: challenge not found")

// lockedName replaces the real name of a challenge shown with the masked anonymize flag, so its
// identity is withheld while its existence (and place on the board) is not.
const lockedName = "???"

var Module = fx.Module("catalog", fx.Provide(New))

type Service struct {
	q *db.Queries
}

func New(pool *pgxpool.Pool) *Service { return &Service{q: db.New(pool)} }

// Listing is one row of the board.
type Listing struct {
	ID         int64
	Name       string
	Category   string
	Value      int32
	Function   string
	SolveCount int64
	Solved     bool
	// Locked is set when this account has not met the challenge's prerequisites and the anonymize
	// flag shows the row anyway. A locked row carries no solvable content and cannot be attempted.
	Locked bool
}

// Challenge is the detail view of a single challenge.
type Challenge struct {
	ID             int64
	Name           string
	Category       string
	Description    string
	Attribution    *string
	ConnectionInfo *string
	Type           string
	Value          int32
	Function       string
	MaxAttempts    int32
	State          string
	SolveCount     int64
	Solved         bool
	// Locked mirrors Listing.Locked: a visible-but-locked detail withholds description, files and
	// hints. A challenge the anonymize flag hides is a not-found instead, never a locked detail.
	Locked bool
	// FlagMode is how this challenge's flags are issued. ModeUnique is the one case where an
	// eligible account's view of the detail assigns it an instance — the caller decides who is
	// eligible; this package only reports the mode.
	FlagMode flags.Mode
	// NextID is the suggested-next challenge, carried on the unlocked detail only. A locked stub
	// withholds it: a challenge whose prerequisites are unmet must not advertise the graph.
	NextID *int64
}

type File struct {
	ID        int64
	Name      string
	SizeBytes int64
}

// Hint carries the price and whether this account already paid it; the content is bought through
// gameplay.UnlockHint and never read here.
type Hint struct {
	ID       int64
	Title    *string
	Cost     int32
	Unlocked bool
	// Locked is set when a prerequisite hint has not been unlocked, so UnlockHint would reject it.
	Locked bool
}

// Detail is one challenge plus the metadata a detail view renders.
type Detail struct {
	Challenge Challenge
	Tags      []string
	Files     []File
	Hints     []Hint
}

// Solve is one entry in a challenge's solve list.
type Solve struct {
	Name  string
	Value int32
	Date  time.Time
}

// SolveCursor is the keyset position in a challenge's solve list: the (date, id) of the last row
// the previous page READ — not the last one it showed. A page whose solvers are all hidden shows
// nothing and must still advance, or the next request re-reads the same rows forever.
type SolveCursor struct {
	Date time.Time
	ID   int64
}

// SolvesPage is one page of a solve list plus the cursor to resume from. Next is nil at the end.
type SolvesPage struct {
	Rows []Solve
	Next *SolveCursor
}

// cutoff is the freeze horizon: a non-nil value hides solves at or after it. nil means live.
func cutoffArg(cutoff *time.Time) pgtype.Timestamptz {
	if cutoff == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *cutoff, Valid: true}
}

func (s *Service) List(ctx context.Context, userID int64, teamID *int64, cutoff *time.Time) ([]Listing, error) {
	rows, err := s.q.ListChallenges(ctx, db.ListChallengesParams{
		UserID: userID, TeamID: teamID, Cutoff: cutoffArg(cutoff),
	})
	if err != nil {
		return nil, fmt.Errorf("catalog: list: %w", err)
	}
	out := make([]Listing, 0, len(rows))
	for _, r := range rows {
		item := Listing{
			ID: r.ID, Name: r.Name, Category: r.Category, Value: r.Value,
			Function: r.Function, SolveCount: r.SolveCount, Solved: r.Solved,
		}
		if !r.PrereqsMet {
			reqs, perr := prereq.Parse(r.Requirements)
			if perr != nil {
				return nil, fmt.Errorf("catalog: list: challenge %d: %w", r.ID, perr)
			}
			if !reqs.Visibility.Visible() {
				continue // hidden until the prerequisites are solved
			}
			item.Locked = true
			if reqs.Visibility == prereq.Masked {
				item.Name = lockedName
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) Detail(ctx context.Context, challengeID, userID int64, teamID *int64, cutoff *time.Time) (Detail, error) {
	ch, err := s.q.GetChallengeForView(ctx, db.GetChallengeForViewParams{
		ChallengeID: challengeID, UserID: userID, TeamID: teamID, Cutoff: cutoffArg(cutoff),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
	}
	if err != nil {
		return Detail{}, fmt.Errorf("catalog: detail: %w", err)
	}

	// A flag_mode we cannot parse must not degrade to 'static': that would hand a shared, empty
	// challenge to every viewer of a unique one and silently kill the uniqueness property.
	mode, err := flags.ParseMode(ch.FlagMode)
	if err != nil {
		return Detail{}, fmt.Errorf("catalog: detail: challenge %d: %w", challengeID, err)
	}

	if !ch.PrereqsMet {
		reqs, perr := prereq.Parse(ch.Requirements)
		if perr != nil {
			return Detail{}, fmt.Errorf("catalog: detail: challenge %d: %w", challengeID, perr)
		}
		// A hidden locked challenge must not disclose its existence, so it is a not-found exactly
		// like a hidden or missing one. A visible-but-locked one returns a stub with no description,
		// connection info, tags, files or hints — nothing that helps solve it.
		if !reqs.Visibility.Visible() {
			return Detail{}, fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
		}
		name := ch.Name
		if reqs.Visibility == prereq.Masked {
			name = lockedName
		}
		return Detail{
			Challenge: Challenge{
				ID: ch.ID, Name: name, Category: ch.Category, Type: ch.Type, Value: ch.Value,
				Function: ch.Function, MaxAttempts: ch.MaxAttempts, State: ch.State,
				SolveCount: ch.SolveCount, Solved: ch.Solved, Locked: true, FlagMode: mode,
			},
			Tags:  []string{},
			Files: []File{},
			Hints: []Hint{},
		}, nil
	}

	tags, err := s.q.ListChallengeTags(ctx, challengeID)
	if err != nil {
		return Detail{}, fmt.Errorf("catalog: detail tags: %w", err)
	}
	files, err := s.q.ListChallengeFiles(ctx, &challengeID)
	if err != nil {
		return Detail{}, fmt.Errorf("catalog: detail files: %w", err)
	}
	hints, err := s.q.ListChallengeHints(ctx, db.ListChallengeHintsParams{
		ChallengeID: challengeID, UserID: userID, TeamID: teamID,
	})
	if err != nil {
		return Detail{}, fmt.Errorf("catalog: detail hints: %w", err)
	}

	d := Detail{
		Challenge: Challenge{
			ID: ch.ID, Name: ch.Name, Category: ch.Category, Description: ch.Description,
			Attribution: ch.Attribution, ConnectionInfo: ch.ConnectionInfo, Type: ch.Type,
			Value: ch.Value, Function: ch.Function, MaxAttempts: ch.MaxAttempts, State: ch.State,
			SolveCount: ch.SolveCount, Solved: ch.Solved, FlagMode: mode, NextID: ch.NextID,
		},
		Tags:  tags,
		Files: make([]File, len(files)),
		Hints: make([]Hint, len(hints)),
	}
	for i, f := range files {
		d.Files[i] = File{ID: f.ID, Name: f.Name, SizeBytes: f.SizeBytes}
	}
	for i, h := range hints {
		d.Hints[i] = Hint{ID: h.ID, Title: h.Title, Cost: h.Cost, Unlocked: h.Unlocked, Locked: h.Locked}
	}
	return d, nil
}

// Solves returns one page of who solved a challenge, oldest first, hiding solves at or after cutoff.
// after is the cursor from the previous page, or nil for the first; limit is the page size, bounded
// by the caller. It reports ErrChallengeNotFound for a challenge that is not visible, the same as
// Detail — the query returns no rows for that case, and rows with shown=false when the challenge
// exists but a solve must not appear (no visible solves, or a hidden/banned solver).
func (s *Service) Solves(
	ctx context.Context, challengeID int64, cutoff *time.Time, after *SolveCursor, limit int,
) (SolvesPage, error) {
	var (
		afterDate pgtype.Timestamptz
		afterID   *int64
	)
	if after != nil {
		afterDate = pgtype.Timestamptz{Time: after.Date, Valid: true}
		id := after.ID
		afterID = &id
	}

	rows, err := s.q.ListChallengeSolves(ctx, db.ListChallengeSolvesParams{
		ChallengeID: challengeID, Cutoff: cutoffArg(cutoff),
		AfterDate: afterDate, AfterID: afterID,
		Lim: int32(limit), //nolint:gosec // limit is bounded by the handler's schema
	})
	if err != nil {
		return SolvesPage{}, fmt.Errorf("catalog: solves: challenge %d: %w", challengeID, err)
	}
	if len(rows) == 0 {
		return SolvesPage{}, fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
	}

	page := SolvesPage{Rows: make([]Solve, 0, len(rows))}
	for _, r := range rows {
		if !r.Shown {
			continue
		}
		page.Rows = append(page.Rows, Solve{Name: r.Name, Value: r.Value, Date: r.Date.Time})
	}
	// A full page may have more behind it; a short one is the tail. The last row is a solve only
	// when the challenge actually had one — the no-solves case is a single row of NULLs.
	if last := rows[len(rows)-1]; len(rows) == limit && last.SolveID != nil {
		page.Next = &SolveCursor{Date: last.Date.Time, ID: *last.SolveID}
	}
	return page, nil
}
