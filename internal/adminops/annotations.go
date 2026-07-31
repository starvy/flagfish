package adminops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/annotation"
)

// An Annotation is one (key, value) pair on a challenge.
type Annotation struct {
	Key   string
	Value string
}

// SetAnnotation writes one annotation, creating it or replacing the value it had. Idempotent by
// construction: the arbiter is UNIQUE(challenge_id, key), so a repeat and a race both settle on one
// row rather than on whichever writer read first.
//
// The key and the value are validated here rather than at the handler because this is the boundary
// every operator write crosses — the CLI and any future importer arrive here too, and a rule that
// only one caller applies is a rule the next caller will miss.
func (s *Service) SetAnnotation(
	ctx context.Context, actor audit.Actor, challengeID int64, key, value string,
) (Annotation, error) {
	k, err := annotation.ParseKey(key)
	if err != nil {
		return Annotation{}, invalidf("%s", err.Error())
	}
	clean, err := annotation.Clean(k, value)
	if err != nil {
		return Annotation{}, invalidf("%s", err.Error())
	}

	var out Annotation
	err = s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		row, setErr := q.AdminSetAnnotation(ctx, db.AdminSetAnnotationParams{
			ChallengeID: challengeID, Key: string(k), Value: clean,
		})
		if setErr != nil {
			var pgErr *pgconn.PgError
			if errors.As(setErr, &pgErr) && pgErr.Code == "23503" {
				return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
			}
			return fmt.Errorf("adminops: set annotation %q on challenge %d: %w", k, challengeID, setErr)
		}
		out = Annotation{Key: row.Key, Value: row.Value}
		return nil
	})
	return out, err
}

// RemoveAnnotation drops one key from one challenge. Zero rows means there was nothing to drop — a
// missing challenge and a key it never carried answer the same way, as with a tag.
func (s *Service) RemoveAnnotation(ctx context.Context, actor audit.Actor, challengeID int64, key string) error {
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		n, err := q.AdminRemoveAnnotation(ctx, db.AdminRemoveAnnotationParams{
			ChallengeID: challengeID, Key: key,
		})
		if err != nil {
			return fmt.Errorf("adminops: remove annotation %q from challenge %d: %w", key, challengeID, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %q on challenge %d", ErrAnnotationNotFound, key, challengeID)
		}
		return nil
	})
}

// ListAnnotations returns one challenge's annotations, key order. Read-only, so no transaction and
// no actor: nothing to audit.
func (s *Service) ListAnnotations(ctx context.Context, challengeID int64) ([]Annotation, error) {
	rows, err := s.q.AdminListChallengeAnnotations(ctx, challengeID)
	if err != nil {
		return nil, fmt.Errorf("adminops: list annotations for challenge %d: %w", challengeID, err)
	}
	out := make([]Annotation, len(rows))
	for i, r := range rows {
		out[i] = Annotation{Key: r.Key, Value: r.Value}
	}
	return out, nil
}
