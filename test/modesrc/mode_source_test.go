//go:build integration

// Package modesrc pins the source of truth for the play mode.
//
// The account model has one home: instance.user_mode, the same row the gameplay SQL
// keys on. config.Snapshot.Mode is derived from it, never from a config user_mode
// key. Every test here fails if that sourcing regresses — if Mode ever drifts back to
// reading the config table, the seams that decide user-vs-team attribution stop
// agreeing, and that is the class of bug this suite exists to catch.
//
// Real Postgres, its own database (MODESRC_DATABASE_URL, or the shared test DB when
// unset). Each test truncates and re-seeds, so the suite owns whatever database it
// runs against.
package modesrc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
)

// stubInserter stands in for the River insert-only client: the announcement enqueue is
// not what these tests assert on.
type stubInserter struct{}

func (stubInserter) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, nil
}

func dial(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("MODESRC_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("neither MODESRC_DATABASE_URL nor TEST_DATABASE_URL is set — run `task test-modesrc`")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	truncate(t, pool)
	return pool
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `
SELECT quote_ident(tablename)
  FROM pg_tables
 WHERE schemaname = 'public'
   AND tablename <> 'goose_db_version'
   AND tablename NOT LIKE 'river%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("no tables to truncate — is the database migrated? try `task test-modesrc`")
	}
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE "+strings.Join(tables, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func seedInstance(t *testing.T, pool *pgxpool.Pool, mode account.Mode) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO instance (user_mode, version) VALUES ($1,'test')`, mode.String()); err != nil {
		t.Fatalf("seed instance %s: %v", mode, err)
	}
}

func seedConfig(t *testing.T, pool *pgxpool.Pool, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO config (key, value) VALUES ($1,$2)`, k, v); err != nil {
			t.Fatalf("seed config %s: %v", k, err)
		}
	}
}

func loadSnapshot(t *testing.T, pool *pgxpool.Pool) (*config.Snapshot, error) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := config.New(context.Background(), config.NewPGStore(pool), log)
	if err != nil {
		return nil, err
	}
	return m.Current(), nil
}

// The mode the whole system plays under is the instance's — read straight from the
// singleton, with no config user_mode key in sight.
func TestSnapshotModeComesFromInstance(t *testing.T) {
	for _, mode := range []account.Mode{account.ModeUsers, account.ModeTeams} {
		t.Run(mode.String(), func(t *testing.T) {
			pool := dial(t)
			seedInstance(t, pool, mode)
			seedConfig(t, pool, map[string]string{"setup": "true"})

			snap, err := loadSnapshot(t, pool)
			if err != nil {
				t.Fatalf("config load: %v", err)
			}
			if snap.Mode != mode {
				t.Fatalf("Snapshot.Mode = %s, want %s — the mode must come from instance.user_mode", snap.Mode, mode)
			}
		})
	}
}

// A config user_mode key never sources Mode: when it agrees with the instance it is
// tolerated for round-tripping, and the mode is still the instance's.
func TestAgreeingConfigUserModeIsHarmless(t *testing.T) {
	for _, mode := range []account.Mode{account.ModeUsers, account.ModeTeams} {
		t.Run(mode.String(), func(t *testing.T) {
			pool := dial(t)
			seedInstance(t, pool, mode)
			seedConfig(t, pool, map[string]string{"setup": "true", "user_mode": mode.String()})

			snap, err := loadSnapshot(t, pool)
			if err != nil {
				t.Fatalf("config load: %v", err)
			}
			if snap.Mode != mode {
				t.Fatalf("Snapshot.Mode = %s, want %s", snap.Mode, mode)
			}
		})
	}
}

// A config user_mode that contradicts the instance is the fingerprint of a corrupted
// instance, and a corrupted instance must not boot. Loud beats silent.
func TestDisagreeingConfigUserModeRefusesToBoot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		instance account.Mode
		cfg      string
	}{
		{"instance teams, config users", account.ModeTeams, "users"},
		{"instance users, config teams", account.ModeUsers, "teams"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := dial(t)
			seedInstance(t, pool, tc.instance)
			seedConfig(t, pool, map[string]string{"setup": "true", "user_mode": tc.cfg})

			_, err := loadSnapshot(t, pool)
			if err == nil {
				t.Fatal("boot succeeded on a config user_mode that disagrees with the instance; it must fail loudly")
			}
			if !strings.Contains(err.Error(), "user_mode") {
				t.Fatalf("boot error %q does not name user_mode — an operator cannot act on it", err)
			}
		})
	}
}

// Before setup there is no instance row and no mode to speak of: the boot must still
// succeed, falling back to the default rather than inventing an authority.
func TestPreSetupBootFallsBackToDefault(t *testing.T) {
	pool := dial(t)
	// No instance row, no setup key.

	snap, err := loadSnapshot(t, pool)
	if err != nil {
		t.Fatalf("pre-setup boot must succeed: %v", err)
	}
	if snap.Mode != account.ModeUsers {
		t.Fatalf("Snapshot.Mode = %s, want the default (users) before setup", snap.Mode)
	}
}

// The end-to-end payoff: a submit built from the instance-sourced snapshot attributes
// the solve to the account the instance dictates. In teams mode a teamless user has no
// account and cannot solve; had Mode regressed to reading a users-mode config, the
// teamless submit below would attribute a solve to the user and this test would fail.
func TestSubmitAttributesToInstanceMode(t *testing.T) {
	ctx := context.Background()

	t.Run("teams", func(t *testing.T) {
		pool := dial(t)
		seedInstance(t, pool, account.ModeTeams)
		seedConfig(t, pool, map[string]string{"setup": "true"})

		snap, err := loadSnapshot(t, pool)
		if err != nil {
			t.Fatalf("config load: %v", err)
		}
		svc := gameplay.New(pool, stubInserter{}, snap.Mode)

		chID := seedChallenge(t, pool, "Teams chal")
		seedFlag(t, pool, chID, "flag{teams}")
		teamID := seedTeam(t, pool, "Wombats")
		onTeam := seedUser(t, pool, "member@ctf.test", &teamID)
		teamless := seedUser(t, pool, "loner@ctf.test", nil)

		// A teamless user has no account under teams mode: the submit is refused, not solved.
		_, teamlessErr := svc.Submit(ctx, gameplay.SubmitInput{
			ChallengeID: chID,
			Actor:       gameplay.Actor{UserID: teamless},
			Provided:    "flag{teams}",
		})
		if !errors.Is(teamlessErr, account.ErrTeamless) {
			t.Fatalf("teamless submit in teams mode: err = %v, want ErrTeamless", teamlessErr)
		}
		if n := solveCount(t, pool, chID); n != 0 {
			t.Fatalf("a teamless user recorded %d solves in teams mode; want 0", n)
		}

		// A user on a team solves, and the solve is keyed on the team.
		res, err := svc.Submit(ctx, gameplay.SubmitInput{
			ChallengeID: chID,
			Actor:       gameplay.Actor{UserID: onTeam, TeamID: &teamID},
			Provided:    "flag{teams}",
		})
		if err != nil {
			t.Fatalf("team member submit: %v", err)
		}
		if res.Status != gameplay.StatusCorrect {
			t.Fatalf("team member submit: status = %s, want correct", res.Status)
		}
		gotUser, gotTeam := soleSolve(t, pool, chID)
		if gotTeam == nil || *gotTeam != teamID {
			t.Fatalf("solve team_id = %v, want %d — teams-mode attribution keys on the team", gotTeam, teamID)
		}
		if gotUser != onTeam {
			t.Fatalf("solve user_id = %d, want %d", gotUser, onTeam)
		}
	})

	t.Run("users", func(t *testing.T) {
		pool := dial(t)
		seedInstance(t, pool, account.ModeUsers)
		seedConfig(t, pool, map[string]string{"setup": "true"})

		snap, err := loadSnapshot(t, pool)
		if err != nil {
			t.Fatalf("config load: %v", err)
		}
		svc := gameplay.New(pool, stubInserter{}, snap.Mode)

		chID := seedChallenge(t, pool, "Users chal")
		seedFlag(t, pool, chID, "flag{users}")
		userID := seedUser(t, pool, "solo@ctf.test", nil)

		res, err := svc.Submit(ctx, gameplay.SubmitInput{
			ChallengeID: chID,
			Actor:       gameplay.Actor{UserID: userID},
			Provided:    "flag{users}",
		})
		if err != nil {
			t.Fatalf("user submit: %v", err)
		}
		if res.Status != gameplay.StatusCorrect {
			t.Fatalf("user submit: status = %s, want correct", res.Status)
		}
		gotUser, gotTeam := soleSolve(t, pool, chID)
		if gotUser != userID {
			t.Fatalf("solve user_id = %d, want %d — users-mode attribution keys on the user", gotUser, userID)
		}
		if gotTeam != nil {
			t.Fatalf("solve team_id = %v, want NULL in users mode", gotTeam)
		}
	})
}

func seedChallenge(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO challenges (name, category, value) VALUES ($1,'misc',100) RETURNING id`,
		name).Scan(&id); err != nil {
		t.Fatalf("seed challenge: %v", err)
	}
	return id
}

func seedFlag(t *testing.T, pool *pgxpool.Pool, challengeID int64, content string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO flags (challenge_id, type, content) VALUES ($1,'static',$2)`,
		challengeID, content); err != nil {
		t.Fatalf("seed flag: %v", err)
	}
}

func seedTeam(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO teams (name, email) VALUES ($1,$2) RETURNING id`,
		name, name+"@team.test").Scan(&id); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	return id
}

func seedUser(t *testing.T, pool *pgxpool.Pool, email string, teamID *int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (name, email, team_id) VALUES ($1,$2,$3) RETURNING id`,
		email, email, teamID).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func soleSolve(t *testing.T, pool *pgxpool.Pool, challengeID int64) (userID int64, teamID *int64) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT user_id, team_id FROM solves WHERE challenge_id = $1`, challengeID).Scan(&userID, &teamID); err != nil {
		t.Fatalf("read solve: %v", err)
	}
	return userID, teamID
}

func solveCount(t *testing.T, pool *pgxpool.Pool, challengeID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM solves WHERE challenge_id = $1`, challengeID).Scan(&n); err != nil {
		t.Fatalf("count solves: %v", err)
	}
	return n
}
