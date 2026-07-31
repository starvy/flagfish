package importer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/db"
)

// importLockKey serialises restores against each other, belt-and-braces over the tasks partial
// unique index. Arbitrary but fixed.
const importLockKey int64 = 0x696d706f7274 // "import"

// truncated is every table the restore wipes, in one statement. It is a compile-time constant — never
// derived from the archive — so the archive can never name a table to clear. tasks, the migration
// bookkeeping and River's own tables are deliberately excluded.
var truncated = []string{
	"instance", "config", "brackets", "teams", "users", "fields", "field_entries",
	"api_tokens", "tracking", "challenges", "files", "tags", "challenge_annotations", "flags", "hints",
	"challenge_instances", "flag_issues", "submissions", "solves", "awards", "hint_unlocks",
	"notifications", "audit_log", "sessions", "email_tokens", "rate_limits",
}

// resyncTables are the tables loaded with explicit ids: their id sequence must be advanced past the
// max, or the next insert collides. config is loaded without an id, so its sequence needs no resync.
var resyncTables = []string{
	"brackets", "teams", "users", "challenges", "files", "flags", "tags", "hints",
	"submissions", "solves", "awards",
}

// Load restores a Plan in a single transaction. Any error rolls the whole thing back, so a failed
// import leaves the instance exactly as it was — the property the whole design turns on.
func Load(ctx context.Context, pool *pgxpool.Pool, plan *Plan) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("import: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; the safety net otherwise

	// replica suppresses foreign keys and the audit triggers for the duration of the COPY: tables can
	// load in any order and a million-row restore emits no audit rows. Unique and check constraints
	// stay live, so a conflicting archive still fails atomically.
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		return fmt.Errorf("import: suppress triggers: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, importLockKey); err != nil {
		return fmt.Errorf("import: lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `TRUNCATE `+identList(truncated)+` RESTART IDENTITY CASCADE`); err != nil {
		return fmt.Errorf("import: truncate: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO instance (id, user_mode, version) VALUES (true, $1, $2)`,
		plan.UserMode, plan.Version); err != nil {
		return fmt.Errorf("import: instance: %w", err)
	}

	q := db.New(tx)
	if err := copyAll(ctx, q, plan); err != nil {
		return err
	}

	for _, t := range resyncTables {
		// pg_get_serial_sequence takes the table name as text; the MAX(id) source identifier comes from
		// this compile-time list, never the archive.
		stmt := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s','id'), GREATEST(COALESCE((SELECT MAX(id) FROM %s),0)+1,1), false)`,
			t, t,
		)
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("import: resync sequence for %s: %w", t, err)
		}
	}

	// Re-enabling the role ignores the rows already inserted, so dangling references are counted
	// explicitly rather than trusted to validate on their own.
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = DEFAULT`); err != nil {
		return fmt.Errorf("import: restore triggers: %w", err)
	}
	if err := validateFKs(ctx, q); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("import: commit: %w", err)
	}
	return nil
}

func copyAll(ctx context.Context, q *db.Queries, plan *Plan) error {
	type step struct {
		name string
		fn   func() (int64, error)
	}
	steps := []step{
		{"brackets", func() (int64, error) { return q.ImportBrackets(ctx, plan.Brackets) }},
		{"teams", func() (int64, error) { return q.ImportTeams(ctx, plan.Teams) }},
		{"users", func() (int64, error) { return q.ImportUsers(ctx, plan.Users) }},
		{"challenges", func() (int64, error) { return q.ImportChallenges(ctx, plan.Challenges) }},
		{"files", func() (int64, error) { return q.ImportFiles(ctx, plan.Files) }},
		{"flags", func() (int64, error) { return q.ImportFlags(ctx, plan.Flags) }},
		{"tags", func() (int64, error) { return q.ImportTags(ctx, plan.Tags) }},
		{"hints", func() (int64, error) { return q.ImportHints(ctx, plan.Hints) }},
		{"submissions", func() (int64, error) { return q.ImportSubmissions(ctx, plan.Submissions) }},
		{"solves", func() (int64, error) { return q.ImportSolves(ctx, plan.Solves) }},
		{"awards", func() (int64, error) { return q.ImportAwards(ctx, plan.Awards) }},
		{"config", func() (int64, error) { return q.ImportConfig(ctx, plan.Config) }},
	}
	for _, s := range steps {
		if _, err := s.fn(); err != nil {
			return fmt.Errorf("import: copy %s: %w", s.name, err)
		}
	}
	return nil
}

func validateFKs(ctx context.Context, q *db.Queries) error {
	checks := []struct {
		name string
		fn   func() (int64, error)
	}{
		{"flags -> challenges", func() (int64, error) { return q.CountFlagsWithoutChallenge(ctx) }},
		{"tags -> challenges", func() (int64, error) { return q.CountTagsWithoutChallenge(ctx) }},
		{"hints -> challenges", func() (int64, error) { return q.CountHintsWithoutChallenge(ctx) }},
		{"files -> challenges", func() (int64, error) { return q.CountFilesWithoutChallenge(ctx) }},
		{"users -> teams", func() (int64, error) { return q.CountUsersWithoutTeam(ctx) }},
		{"teams -> captain", func() (int64, error) { return q.CountTeamsWithoutCaptain(ctx) }},
		{"submissions -> parents", func() (int64, error) { return q.CountSubmissionsWithoutParent(ctx) }},
		{"solves -> parents", func() (int64, error) { return q.CountSolvesWithoutParent(ctx) }},
		{"awards -> parents", func() (int64, error) { return q.CountAwardsWithoutParent(ctx) }},
	}
	for _, c := range checks {
		n, err := c.fn()
		if err != nil {
			return fmt.Errorf("import: validate %s: %w", c.name, err)
		}
		if n > 0 {
			return fmt.Errorf("import: %d dangling reference(s) in %s: the archive is internally inconsistent", n, c.name)
		}
	}
	return nil
}

func identList(tables []string) string {
	out := ""
	for i, t := range tables {
		if i > 0 {
			out += ", "
		}
		out += t
	}
	return out
}
