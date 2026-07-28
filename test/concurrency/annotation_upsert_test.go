//go:build integration

package concurrency

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/domain/account"
)

// Setting an annotation is an upsert, and UNIQUE(challenge_id, key) is its arbiter. Under N
// concurrent writers of the SAME key the row count must stay one: the check-then-insert this
// replaces would let two writers both read "absent" and both insert, and the failure is a duplicate
// key that no read path expects — a challenge with two countries.
func TestAnnotationUpsertSettlesOnOneRow(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := context.Background()
	ops := adminops.New(f.pool)

	chID := f.seedChallenge(challengeSpec{Name: "Contested", Value: 100, Flag: "flag{x}"})

	// Every writer sets the same key to a different value, so the survivor is whichever committed
	// last — the point is that there IS exactly one survivor, not which it is.
	codes := []string{"CZ", "SK", "PL", "AT", "DE", "JP", "US", "GB", "FR", "IT"}
	errs := race(N, func(i int) error {
		_, err := ops.SetAnnotation(ctx, audit.Actor{ID: 1}, chID, "country", codes[i%len(codes)])
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed: %v — a concurrent upsert must not error, that is what ON CONFLICT is for", i, err)
		}
	}

	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM challenge_annotations WHERE challenge_id = $1 AND key = 'country'`,
		chID).Scan(&rows); err != nil {
		t.Fatalf("count annotations: %v", err)
	}
	if rows != 1 {
		t.Fatalf("challenge_annotations holds %d rows for one key, want exactly 1", rows)
	}

	// And the survivor is one of the values actually written, not a torn read.
	var got string
	if err := f.pool.QueryRow(ctx,
		`SELECT value FROM challenge_annotations WHERE challenge_id = $1 AND key = 'country'`,
		chID).Scan(&got); err != nil {
		t.Fatalf("read annotation: %v", err)
	}
	if !slices.Contains(codes, got) {
		t.Fatalf("stored value %q was never written by any writer", got)
	}
}

// A set and a remove of the same key, racing. Whichever order the database picks, the outcome has to
// be one of the two coherent ones — never a row that is half gone, and never an error that leaves
// the caller unable to say what happened.
func TestAnnotationSetAndRemoveRace(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := context.Background()
	ops := adminops.New(f.pool)

	chID := f.seedChallenge(challengeSpec{Name: "Flipped", Value: 100, Flag: "flag{x}"})

	// Half the slots set the key, half remove it.
	errs := race(N, func(i int) error {
		if i%2 == 0 {
			_, err := ops.SetAnnotation(ctx, audit.Actor{ID: 1}, chID, "country", "CZ")
			return err
		}
		err := ops.RemoveAnnotation(ctx, audit.Actor{ID: 1}, chID, "country")
		// Removing a key that is not there is a not-found, which is a legitimate outcome of this
		// race and not a failure.
		if !isAnnotationNotFound(err) && err != nil {
			return err
		}
		return nil
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("slot %d failed: %v", i, err)
		}
	}

	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM challenge_annotations WHERE challenge_id = $1 AND key = 'country'`,
		chID).Scan(&rows); err != nil {
		t.Fatalf("count annotations: %v", err)
	}
	if rows > 1 {
		t.Fatalf("challenge_annotations holds %d rows for one key, want 0 or 1", rows)
	}
}

func isAnnotationNotFound(err error) bool {
	return errors.Is(err, adminops.ErrAnnotationNotFound)
}
