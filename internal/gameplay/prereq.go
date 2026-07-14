package gameplay

import (
	"context"
	"fmt"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/prereq"
)

// challengePrereqsMet reports whether the actor has solved every prerequisite challenge in reqs.
// It is a lockless read: the submit path calls it before taking the challenge lock, so a
// prerequisite-locked challenge is rejected without ever touching the hot lock.
func (s *Service) challengePrereqsMet(ctx context.Context, q *db.Queries, actor Actor, reqs prereq.Requirements) (bool, error) {
	ids := dedupe(reqs.Prerequisites)
	if len(ids) == 0 {
		return true, nil
	}
	have, err := q.CountSolvedPrerequisites(ctx, db.CountSolvedPrerequisitesParams{
		Prerequisites: ids,
		UserID:        actor.UserID,
		TeamID:        actor.TeamID,
	})
	if err != nil {
		return false, fmt.Errorf("count solved prerequisites: %w", err)
	}
	return have == int64(len(ids)), nil
}

// hintPrereqsMet reports whether the actor has unlocked every prerequisite hint in reqs.
func (s *Service) hintPrereqsMet(ctx context.Context, q *db.Queries, actor Actor, reqs prereq.Requirements) (bool, error) {
	ids := dedupe(reqs.Prerequisites)
	if len(ids) == 0 {
		return true, nil
	}
	have, err := q.CountUnlockedHintPrerequisites(ctx, db.CountUnlockedHintPrerequisitesParams{
		Prerequisites: ids,
		UserID:        actor.UserID,
		TeamID:        actor.TeamID,
	})
	if err != nil {
		return false, fmt.Errorf("count unlocked hint prerequisites: %w", err)
	}
	return have == int64(len(ids)), nil
}

// dedupe collapses a prerequisite id list to its distinct set, so the count comparison is exact
// even if an author lists the same id twice.
func dedupe(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
