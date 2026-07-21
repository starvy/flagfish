//go:build integration

package concurrency

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"golang.org/x/crypto/argon2"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/domain/account"
)

// cheapEmptyHash mints an Argon2id hash of the empty string at throwaway cost — a few KiB of
// memory instead of the hasher's 64 MiB. It verifies against "" like any real hash but for a
// fraction of the work, which is what lets N goroutines race the team-size cap without each
// paying a full-cost verification.
func cheapEmptyHash(t *testing.T) string {
	t.Helper()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatalf("salt: %v", err)
	}
	const (
		aTime    = 1
		aMemory  = 8 // KiB
		aThreads = 1
	)
	key := argon2.IDKey([]byte(""), salt, aTime, aMemory, aThreads, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, aMemory, aTime, aThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}

// The team_size cap is a config value, so it cannot be a CHECK; the caps trigger serializes the
// count-then-enroll behind the same advisory lock the registration cap uses. N users racing for one
// open slot must therefore see exactly one winner — the rest lose the slot in the database, not in Go.
func TestTeamSlotCap_ExactlyOneWins(t *testing.T) {
	f := setup(t, account.ModeTeams)
	ctx := testCtx(t)

	const slots = 1
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO config (key, value) VALUES ('team_size', $1)`, fmt.Sprint(slots)); err != nil {
		t.Fatalf("set team_size: %v", err)
	}

	// A captainless team with every slot open. Seeding it directly keeps the racers teamless — the
	// harness's seedUser would give each its own team. Every team now holds a join secret (the
	// column is NOT NULL), so the team gets a cheap one: an Argon2id hash of the empty string minted
	// at throwaway cost. It verifies against "" for a few KiB rather than 64 MiB, so N goroutines
	// racing the cap do not each pay a full-cost verification, and the rehash-on-join of "" fails
	// fast rather than minting N real hashes. The race still turns on the cap alone.
	var teamID int64
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO teams (name, email, password_hash) VALUES ('slots', 'slots@ctf.test', $1) RETURNING id`,
		cheapEmptyHash(t)).Scan(&teamID); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	userIDs := make([]int64, N)
	for i := range N {
		if err := f.pool.QueryRow(ctx,
			`INSERT INTO users (name, email) VALUES ($1, $2) RETURNING id`,
			fmt.Sprintf("j%03d", i), fmt.Sprintf("j%03d@ctf.test", i)).Scan(&userIDs[i]); err != nil {
			t.Fatalf("seed user %d: %v", i, err)
		}
	}

	acct := accounts.NewService(f.pool, account.ModeTeams, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// The join secret verifies against an empty password at throwaway cost, so the race turns on the
	// cap alone and not on N argon2 verifications.
	errs := race(N, func(i int) error {
		_, err := acct.JoinTeam(context.Background(), userIDs[i], "slots", "")
		return err
	})

	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, accounts.ErrTeamFull):
			// The slot was taken by the time this transaction's trigger ran: the cap holding.
		default:
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	if won != slots {
		t.Errorf("winners = %d, want exactly %d", won, slots)
	}
	if n := f.count(`SELECT count(*) FROM users WHERE team_id = $1`, teamID); n != slots {
		t.Errorf("members = %d, want %d — the team_size cap was bypassed", n, slots)
	}
}
