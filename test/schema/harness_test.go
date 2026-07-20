//go:build integration

// Package schema holds the standing inventory tests: mechanical sweeps that pin every
// admin-writable column, every registered config key, and every ledger foreign key to a
// deliberate decision — a writer, a reader, a guard, or an explicit exemption.
//
// A failing subtest here is a named backlog item, not a broken build. The suite reads
// SCHEMA_DATABASE_URL with no fallback so the required sweeps (`task test-race` and
// friends, which set only TEST_DATABASE_URL) skip it and stay green while the gaps are
// being closed. Run with `task test-schema`.
package schema

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

// connect opens a plain connection to the migrated schema-suite database, or skips.
func connect(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("SCHEMA_DATABASE_URL")
	if dsn == "" {
		t.Skip("SCHEMA_DATABASE_URL is not set — run `task test-schema`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// repoPath resolves a repo-root-relative path from this package's directory and fails
// loudly if it does not exist, so a moved source tree breaks the sweep instead of
// silently emptying it.
func repoPath(t *testing.T, elem ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{"..", ".."}, elem...)...)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("repo path %s: %v", filepath.Join(elem...), err)
	}
	return p
}
