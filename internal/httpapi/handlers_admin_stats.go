package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
)

// statsBuckets is the closed set of timeline widths. It doubles as the injection guard: only a value
// from this map reaches date_trunc, so the bucket is a bound parameter and never interpolated SQL.
var statsBuckets = map[string]bool{
	"hour": true, "day": true, "week": true, "month": true,
}

type adminStatsInput struct {
	Bucket string `query:"bucket" default:"day" doc:"timeline granularity: hour|day|week|month"`
}

type statTypeCount struct {
	Type  string `json:"type"`
	Count int64  `json:"count"`
}

type statChallengeSolves struct {
	ChallengeID int64  `json:"challenge_id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Value       int32  `json:"value"`
	SolveCount  int64  `json:"solve_count"`
}

type statTimeBucket struct {
	Bucket time.Time `json:"bucket"`
	Count  int64     `json:"count"`
}

type adminStatsOutput struct {
	Body struct {
		Totals struct {
			Solves           int64 `json:"solves"`
			Submissions      int64 `json:"submissions"`
			Awards           int64 `json:"awards"`
			SolvedChallenges int64 `json:"solved_challenges"`
		} `json:"totals"`
		SubmissionsByType []statTypeCount       `json:"submissions_by_type"`
		ChallengeSolves   []statChallengeSolves `json:"challenge_solves"`
		SolvesOverTime    []statTimeBucket      `json:"solves_over_time"`
		Bucket            string                `json:"bucket"`
	}
}

func (s *Server) registerAdminStats() {
	Register(s.Admin, policy.ClassStatistics, huma.Operation{
		OperationID: "admin-stats", Method: http.MethodGet, Path: "/stats",
		Summary: "Dashboard aggregates: totals, submissions by type, per-challenge solves, solves over time",
		Tags:    []string{"admin/stats"},
	}, s.adminStats)
}

func (s *Server) adminStats(ctx context.Context, in *adminStatsInput) (*adminStatsOutput, error) {
	if !statsBuckets[in.Bucket] {
		return nil, huma.Error422UnprocessableEntity("unknown bucket: " + in.Bucket)
	}

	ov, err := s.opts.Stats.Overview(ctx, in.Bucket)
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "stats overview failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load statistics")
	}

	out := &adminStatsOutput{}
	out.Body.Bucket = in.Bucket
	out.Body.Totals.Solves = ov.Totals.Solves
	out.Body.Totals.Submissions = ov.Totals.Submissions
	out.Body.Totals.Awards = ov.Totals.Awards
	out.Body.Totals.SolvedChallenges = ov.Totals.SolvedChallenges

	out.Body.SubmissionsByType = make([]statTypeCount, len(ov.SubmissionsByType))
	for i, r := range ov.SubmissionsByType {
		out.Body.SubmissionsByType[i] = statTypeCount{Type: r.Type, Count: r.Count}
	}
	out.Body.ChallengeSolves = make([]statChallengeSolves, len(ov.ChallengeSolves))
	for i, r := range ov.ChallengeSolves {
		out.Body.ChallengeSolves[i] = statChallengeSolves{
			ChallengeID: r.ChallengeID, Name: r.Name, Category: r.Category,
			Value: r.Value, SolveCount: r.SolveCount,
		}
	}
	out.Body.SolvesOverTime = make([]statTimeBucket, len(ov.SolvesOverTime))
	for i, r := range ov.SolvesOverTime {
		out.Body.SolvesOverTime[i] = statTimeBucket{Bucket: r.Bucket, Count: r.Count}
	}
	return out, nil
}
