// Package stats is the read side of the admin dashboard: pure aggregation over the append-only
// gameplay tables. It is admin-gated because solve distributions are exactly what the scoreboard
// freeze exists to hide. It holds no locks, mutates nothing, and never touches the hot path.
package stats

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/db"
)

var Module = fx.Module("stats", fx.Provide(New))

type Service struct {
	q *db.Queries
}

func New(pool *pgxpool.Pool) *Service { return &Service{q: db.New(pool)} }

// Totals are the dashboard's headline counters.
type Totals struct {
	Solves           int64
	Submissions      int64
	Awards           int64
	SolvedChallenges int64
}

// TypeCount is one submission status and how many attempts carried it.
type TypeCount struct {
	Type  string
	Count int64
}

// ChallengeSolves is one challenge and its solve count — the per-challenge distribution, most-solved
// first, with zero-solve challenges included so the least-solved tail is visible.
type ChallengeSolves struct {
	ChallengeID int64
	Name        string
	Category    string
	Value       int32
	SolveCount  int64
}

// TimeBucket is the solve count in one date_trunc window.
type TimeBucket struct {
	Bucket time.Time
	Count  int64
}

// Overview is the whole dashboard payload.
type Overview struct {
	Totals            Totals
	SubmissionsByType []TypeCount
	ChallengeSolves   []ChallengeSolves
	SolvesOverTime    []TimeBucket
}

// Overview assembles the dashboard in one call. bucket is the date_trunc width for the timeline; the
// caller restricts it to a known set before it reaches the query, so it is a bound parameter and never
// interpolated SQL.
func (s *Service) Overview(ctx context.Context, bucket string) (Overview, error) {
	totals, err := s.q.StatsTotals(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("stats: totals: %w", err)
	}
	byType, err := s.q.StatsSubmissionsByType(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("stats: submissions by type: %w", err)
	}
	perChallenge, err := s.q.StatsChallengeSolves(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("stats: challenge solves: %w", err)
	}
	overTime, err := s.q.StatsSolvesOverTime(ctx, bucket)
	if err != nil {
		return Overview{}, fmt.Errorf("stats: solves over time: %w", err)
	}

	out := Overview{
		Totals: Totals{
			Solves: totals.TotalSolves, Submissions: totals.TotalSubmissions,
			Awards: totals.TotalAwards, SolvedChallenges: totals.SolvedChallenges,
		},
		SubmissionsByType: make([]TypeCount, len(byType)),
		ChallengeSolves:   make([]ChallengeSolves, len(perChallenge)),
		SolvesOverTime:    make([]TimeBucket, len(overTime)),
	}
	for i, r := range byType {
		out.SubmissionsByType[i] = TypeCount{Type: r.Type, Count: r.Count}
	}
	for i, r := range perChallenge {
		out.ChallengeSolves[i] = ChallengeSolves{
			ChallengeID: r.ChallengeID, Name: r.Name, Category: r.Category,
			Value: r.Value, SolveCount: r.SolveCount,
		}
	}
	for i, r := range overTime {
		out.SolvesOverTime[i] = TimeBucket{Bucket: r.Bucket.Time, Count: r.Count}
	}
	return out, nil
}
