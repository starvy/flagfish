package adminops

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/prereq"
)

// ChallengeRequirements is the write shape of the requirements sub-resource.
type ChallengeRequirements struct {
	Prerequisites []int64
	Visibility    prereq.Visibility
}

// SetChallengeRequirements replaces a challenge's prerequisites whole. Duplicates collapse,
// self-reference and dangling ids are refused, and a cycle is stored with a warning rather than
// refused: the runtime gate is a membership test that never traverses, imported archives may
// already carry one, and a jsonb column offers no constraint to make the refusal race-free.
func (s *Service) SetChallengeRequirements(ctx context.Context, actor audit.Actor, challengeID int64, in ChallengeRequirements) (db.Challenge, []string, error) {
	ids := dedupeIDs(in.Prerequisites)
	for _, id := range ids {
		if id == challengeID {
			return db.Challenge{}, nil, invalidf("challenge %d cannot be its own prerequisite", challengeID)
		}
	}

	raw, err := prereq.Encode(prereq.Requirements{Prerequisites: ids, Visibility: in.Visibility})
	if err != nil {
		return db.Challenge{}, nil, fmt.Errorf("adminops: set requirements: %w", err)
	}

	var out db.Challenge
	var warnings []string
	err = s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		if len(ids) > 0 {
			found, ferr := q.AdminFilterChallengeIDs(ctx, ids)
			if ferr != nil {
				return fmt.Errorf("adminops: set requirements: check prerequisites: %w", ferr)
			}
			if missing := missingIDs(ids, found); len(missing) > 0 {
				return invalidf("prerequisites name challenges that do not exist: %s", joinIDs(missing, ", "))
			}
		}
		var uerr error
		out, uerr = q.AdminSetChallengeRequirements(ctx, db.AdminSetChallengeRequirementsParams{
			ChallengeID: challengeID, Requirements: raw,
		})
		if errors.Is(uerr, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
		} else if uerr != nil {
			return fmt.Errorf("adminops: set requirements on challenge %d: %w", challengeID, uerr)
		}
		warnings, uerr = cycleWarnings(ctx, q, challengeID)
		return uerr
	})
	if err != nil {
		return db.Challenge{}, nil, err
	}
	return out, warnings, nil
}

// cycleWarnings reads the whole prerequisite graph — the write it follows included — and names the
// cycle through the just-written challenge, if any.
func cycleWarnings(ctx context.Context, q *db.Queries, challengeID int64) ([]string, error) {
	rows, err := q.AdminListChallengeRequirements(ctx)
	if err != nil {
		return nil, fmt.Errorf("adminops: set requirements: read graph: %w", err)
	}
	edges := make(map[int64][]int64, len(rows))
	for _, r := range rows {
		reqs, perr := prereq.Parse(r.Requirements)
		if perr != nil {
			return nil, fmt.Errorf("adminops: set requirements: challenge %d: %w", r.ID, perr)
		}
		edges[r.ID] = reqs.Prerequisites
	}
	cycle := prereq.FindCycle(edges, challengeID)
	if cycle == nil {
		return nil, nil
	}
	return []string{fmt.Sprintf(
		"prerequisite cycle %s: none of these challenges can unlock until an admin breaks the cycle",
		joinIDs(cycle, " → "),
	)}, nil
}

// validateHintPrereqs refuses prerequisites that are not hints of the same challenge, and
// self-reference. It runs after the write, in the same transaction, so a refusal rolls the write
// back — running it first would misreport a missing parent as a bad prerequisite.
func validateHintPrereqs(ctx context.Context, q *db.Queries, challengeID, hintID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if id == hintID {
			return invalidf("hint %d cannot be its own prerequisite", hintID)
		}
	}
	found, err := q.AdminFilterHintIDs(ctx, db.AdminFilterHintIDsParams{Ids: ids, ChallengeID: challengeID})
	if err != nil {
		return fmt.Errorf("adminops: check hint prerequisites: %w", err)
	}
	if missing := missingIDs(ids, found); len(missing) > 0 {
		return invalidf("hint prerequisites must be hints on the same challenge; unknown or cross-challenge ids: %s",
			joinIDs(missing, ", "))
	}
	return nil
}

func dedupeIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func missingIDs(want, have []int64) []int64 {
	got := make(map[int64]struct{}, len(have))
	for _, id := range have {
		got[id] = struct{}{}
	}
	var missing []int64
	for _, id := range want {
		if _, ok := got[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

func joinIDs(ids []int64, sep string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, sep)
}
