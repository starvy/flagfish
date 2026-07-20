package adminops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
)

// AddTag attaches a tag value to one challenge. There is no pre-check: the UNIQUE and FK
// constraints are the check, and their violations map to the conflict and not-found below.
func (s *Service) AddTag(ctx context.Context, actor audit.Actor, challengeID int64, value string) (db.Tag, error) {
	var out db.Tag
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminAddTag(ctx, db.AdminAddTagParams{ChallengeID: challengeID, Value: value})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				switch pgErr.Code {
				case "23505":
					return fmt.Errorf("%w: %q on challenge %d", ErrTagAlreadyAttached, value, challengeID)
				case "23503":
					return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
				}
			}
			return fmt.Errorf("adminops: add tag %q to challenge %d: %w", value, challengeID, err)
		}
		return nil
	})
	return out, err
}

// RemoveTag detaches one tag value from one challenge. Zero rows means there was nothing to
// detach — a missing challenge and a tag it never carried answer the same way.
func (s *Service) RemoveTag(ctx context.Context, actor audit.Actor, challengeID int64, value string) error {
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		n, err := q.AdminRemoveTag(ctx, db.AdminRemoveTagParams{ChallengeID: challengeID, Value: value})
		if err != nil {
			return fmt.Errorf("adminops: remove tag %q from challenge %d: %w", value, challengeID, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %q on challenge %d", ErrTagNotFound, value, challengeID)
		}
		return nil
	})
}

// ListTags returns every tag value with the number of challenges that carry it. Read-only, so no
// transaction and no actor: nothing to audit.
func (s *Service) ListTags(ctx context.Context) ([]db.AdminListTagsRow, error) {
	rows, err := s.q.AdminListTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("adminops: list tags: %w", err)
	}
	return rows, nil
}

// MergeTag renames one tag value to another across every challenge. It is a merge, not just a
// rename: on a challenge that already carries the destination the source row is dropped rather than
// colliding on UNIQUE(challenge_id, value). Both the drops and the renames are audited.
func (s *Service) MergeTag(ctx context.Context, actor audit.Actor, from, to string) error {
	if from == to {
		return invalidf("the source and destination tag are the same")
	}
	if to == "" {
		return invalidf("the destination tag is empty")
	}
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		collisions, err := q.AdminMergeTagCollisions(ctx, db.AdminMergeTagCollisionsParams{FromValue: from, ToValue: to})
		if err != nil {
			return fmt.Errorf("adminops: merge tag: drop collisions: %w", err)
		}
		renamed, err := q.AdminRenameTag(ctx, db.AdminRenameTagParams{FromValue: from, ToValue: to})
		if err != nil {
			return fmt.Errorf("adminops: merge tag: rename: %w", err)
		}
		if collisions+renamed == 0 {
			return fmt.Errorf("%w: %q", ErrTagNotFound, from)
		}
		return nil
	})
}

// DeleteTag removes a tag value from every challenge that carries it. A tag attached to challenges
// is refused unless force is set, so the destructive whole-board removal is always deliberate.
func (s *Service) DeleteTag(ctx context.Context, actor audit.Actor, value string, force bool) error {
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		uses, err := q.AdminCountTagUses(ctx, value)
		if err != nil {
			return fmt.Errorf("adminops: delete tag: count uses: %w", err)
		}
		if uses == 0 {
			return fmt.Errorf("%w: %q", ErrTagNotFound, value)
		}
		if !force {
			return fmt.Errorf("%w: %q on %d challenge(s)", ErrTagInUse, value, uses)
		}
		if _, err := q.AdminDeleteTag(ctx, value); err != nil {
			return fmt.Errorf("adminops: delete tag: %w", err)
		}
		return nil
	})
}
