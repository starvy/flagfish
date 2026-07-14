//go:build integration

package integration

import (
	"context"
	"math/rand"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/domain/scoring"
)

// The decay curve is evaluated twice in this system, in two different languages:
//
//   - in Go, by scoring.Curve.ValueAt — which stamps solves.value and renders the
//     challenge's asking price;
//   - in SQL, by RecalcChallengeValue — which writes challenges.value under the challenge lock.
//
// If those two ever disagree by a single point, the price we display and the price we
// store diverge — silently, with nothing anywhere to say so, for the rest of the event.
// No unit test on either side can catch that, because each is individually "correct".
//
// So we test the agreement, not the implementations. This is the test that made us throw
// out float8: with (initial=3901, minimum=205, decay=142) the true logarithmic value at
// n == decay is exactly 205, but in float64 the expression lands a hair above the integer
// and CEIL lifts it to 206. Both engines were plausible. Neither was reliable.
//
// Both sides are now pure integer arithmetic (ceil(-x) == -floor(x), and Postgres's
// integer `/` is floor for non-negative operands, which is what Go's truncating `/` does
// on the same values), so they agree by construction. This test is what keeps them that
// way.
func TestDecayAgreesBetweenGoAndPostgres(t *testing.T) {
	// TEST_DATABASE_URL — the name the Taskfile, .env.example and CI all set.
	// This read used to be FLAGFISH_TEST_DATABASE_URL, which nothing sets, so this
	// test skipped in CI for its entire life: the one test standing between us and
	// a silent Go/Postgres decay divergence was never actually run. Hence the
	// t.Fatal below rather than a t.Skip — under `-tags=integration` the caller has
	// declared they intend to hit Postgres, and a skip there is a false green.
	// Loud beats silent.
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL not set; run `task test-integration` (a skip here would be a false green)")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// The expression under test is lifted verbatim from RecalcChallengeValue
	// (internal/db/queries/gameplay.sql). If you change it there, change it here —
	// and this test is what will tell you that you forgot.
	const fn = `
CREATE OR REPLACE FUNCTION pg_value_at(fn text, initial int, minimum int, decay int, solves int)
RETURNS int LANGUAGE sql IMMUTABLE AS $$
SELECT GREATEST(
    minimum::bigint,
    CASE fn
        WHEN 'linear' THEN
            initial::bigint - (decay::bigint * GREATEST(solves - 1, 0)::bigint)
        ELSE
            initial::bigint - (
                ((initial - minimum)::bigint
                 * LEAST(GREATEST(solves - 1, 0), decay)::bigint
                 * LEAST(GREATEST(solves - 1, 0), decay)::bigint)
                / (NULLIF(decay, 0)::bigint * NULLIF(decay, 0)::bigint)
            )
    END
)::int;
$$;`
	if _, err := conn.Exec(ctx, fn); err != nil {
		t.Fatalf("define pg_value_at: %v", err)
	}

	r := rand.New(rand.NewSource(42))
	funcs := []scoring.Function{scoring.FunctionLinear, scoring.FunctionLogarithmic}

	for i := range 5000 {
		initial := r.Intn(5000) + 1
		minimum := r.Intn(initial + 1)
		decay := r.Intn(200) + 1
		// Sweep well past the knee, and sit exactly on it — the boundary is where
		// float8 broke, so it is where this test must be dense.
		var solves int
		switch i % 4 {
		case 0:
			solves = decay // n == decay-1
		case 1:
			solves = decay + 1 // n == decay, the exact-landing case
		case 2:
			solves = decay + 2
		default:
			solves = r.Intn(500)
		}

		for _, f := range funcs {
			c := scoring.Curve{Function: f, Initial: initial, Minimum: minimum, Decay: decay}

			var pg int
			err := conn.QueryRow(ctx, `SELECT pg_value_at($1,$2,$3,$4,$5)`,
				f.String(), initial, minimum, decay, solves).Scan(&pg)
			if err != nil {
				t.Fatalf("query: %v", err)
			}

			if got := c.ValueAt(solves); got != pg {
				t.Fatalf("Go and Postgres disagree: %+v at solves=%d — Go=%d, Postgres=%d",
					c, solves, got, pg)
			}
		}
	}
}
