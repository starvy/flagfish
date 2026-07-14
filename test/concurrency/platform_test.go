//go:build integration

package concurrency

import (
	"crypto/sha256"
	"fmt"
	"net/netip"
	"testing"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
)

// The files.location column names a blob exactly once, so two concurrent uploads of the
// same object must not both insert.
//
// A check-then-insert with no unique constraint behind it lets two concurrent uploads of
// the same object both see "not present" and both insert, leaving `location` ambiguous
// for a column whose whole job is to name a blob exactly once. Here the unique index is
// the arbiter and InsertFileOnce is idempotent through it.
func TestFileLocation_NoDoubleInsert(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	ch := f.seedChallenge(challengeSpec{Value: 100, Flag: correctFlag})
	sum := sha256.Sum256([]byte("the-bytes"))
	const location = "abc123/challenge.zip"

	ids := make([]int64, N)
	errs := race(N, func(i int) error {
		row, err := f.q.InsertFileOnce(ctx, db.InsertFileOnceParams{
			Location:    location,
			Sha256sum:   sum[:],
			SizeBytes:   9,
			ChallengeID: &ch,
		})
		ids[i] = row.ID
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	if n := f.count(`SELECT count(*) FROM files WHERE location = $1`, location); n != 1 {
		t.Errorf("files rows = %d, want 1 — `location` no longer identifies a blob", n)
	}
	// Handing every uploader the same id is what makes this idempotent rather than merely
	// constrained.
	for i, id := range ids {
		if id != ids[0] {
			t.Fatalf("goroutine %d got file id %d, goroutine 0 got %d", i, id, ids[0])
		}
	}
}

// Ratings are deferred out of v1 and there is no `ratings` table, so there is nothing to
// race; inventing one to satisfy a test would be adding scope backwards. The shape of that
// race — an uncaught upsert conflict — is already covered by the file-location and tracking
// cases below, on tables we do have.
//
// This fails loudly if a `ratings` table ever lands without its concurrency test.
func TestRatings_Deferred(t *testing.T) {
	f := setup(t, account.ModeUsers)

	n := f.count(`SELECT count(*) FROM information_schema.tables
                   WHERE table_schema = 'public' AND table_name = 'ratings'`)
	if n != 0 {
		t.Fatal("a `ratings` table now exists, but the ratings upsert race is still " +
			"untested.\n" +
			"    This test only skipped because ratings are deferred out of v1. If they " +
			"have landed,\n" +
			"    write the real case (concurrent rate ⇒ 200, exactly 1 row) and delete this test.")
	}
	t.Log("skipped: ratings are deferred out of v1, so there is no ratings table to race")
}

// The registration cap must hold under concurrent signups.
//
// A count-then-insert against the cap lets concurrent registrations all read the pre-insert
// count, all pass, and blow straight through it. The cap is a config value and so cannot be
// a CHECK; a trigger serializes the count-then-insert behind a transaction-scoped advisory
// lock, which is the only thing that fixes a count-then-insert.
func TestRegistrationCap_Holds(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	const limit = 10
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO config (key, value) VALUES ('num_users', $1)`, fmt.Sprint(limit)); err != nil {
		t.Fatalf("set cap: %v", err)
	}

	errs := race(N, func(i int) error {
		_, err := f.pool.Exec(ctx,
			`INSERT INTO users (name, email) VALUES ($1, $2)`,
			fmt.Sprintf("r%03d", i), fmt.Sprintf("r%03d@ctf.test", i))
		return err
	})

	accepted := 0
	for _, err := range errs {
		if err == nil {
			accepted++
		}
		// The rest raised check_violation from the trigger: the cap doing its job.
	}

	if accepted != limit {
		t.Errorf("registrations accepted = %d, want exactly %d", accepted, limit)
	}
	if n := f.count(`SELECT count(*) FROM users WHERE banned = false AND hidden = false`); n != limit {
		t.Errorf("users = %d, want %d — the cap was bypassed", n, limit)
	}
}

// A tracking-row insert must never cost the player their session.
//
// A check-then-insert of a tracking row on every authenticated request, rolling the session
// back on the integrity error and signing the user out, means opening two tabs at once can
// silently log a player out mid-CTF with nothing in any log to explain it. UNIQUE(user_id, ip)
// makes it one idempotent statement that cannot raise, so there is no error path left to mishandle.
func TestTrackingUpsert_SessionSurvives(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	actor, _ := f.seedUser("tabuser")
	ip := netip.MustParseAddr("203.0.113.7")

	// One user, one IP, N simultaneous requests: the two-tabs scenario, amplified.
	errs := race(N, func(int) error {
		_, err := f.q.UpsertTracking(ctx, db.UpsertTrackingParams{
			UserID: actor.UserID,
			Ip:     ip,
		})
		return err
	})

	// Not one of them may error: an error here would be a logout.
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v — this error path would log the user out mid-event", i, err)
		}
	}
	if n := f.count(`SELECT count(*) FROM tracking WHERE user_id = $1`, actor.UserID); n != 1 {
		t.Errorf("tracking rows = %d, want 1", n)
	}
}

// The submission rate limit cannot lose increments.
//
// There is no counter to lose them: the limit is a COUNT(*) over the append-only submission
// log. A cached counter would have to be atomic on every backend it can run against, and one
// that quietly is not keeps returning 200 while limiting nothing.
func TestRateLimitCounter_IsExactOnPostgres(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	ch := f.seedChallenge(challengeSpec{Value: 100, Flag: correctFlag})
	actor, _ := f.seedUser("bruteforcer")

	// A brute-force burst: the workload the limiter exists for.
	errs := race(N, func(i int) error {
		_, err := f.svc.Submit(ctx, gameplay.SubmitInput{
			ChallengeID: ch, Actor: actor, Provided: fmt.Sprintf("flagfish{guess_%d}", i),
		})
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	got, err := f.q.CountIncorrectSubmissions(ctx, db.CountIncorrectSubmissionsParams{
		ChallengeID: ch,
		UserID:      actor.UserID,
		TeamID:      actor.TeamID,
	})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != N {
		t.Errorf("wrong-answer count = %d, want %d — increments were lost", got, N)
	}

	// The log agrees, because the log IS the counter.
	if n := f.count(`SELECT count(*) FROM submissions WHERE challenge_id = $1`, ch); n != N {
		t.Errorf("submissions = %d, want %d", n, N)
	}
}
