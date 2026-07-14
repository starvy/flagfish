//go:build integration

package importer_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/migrate"
	"github.com/starvy/flagfish/internal/platform/importer"
)

// The importer restore is destructive: it TRUNCATEs the whole instance. So the suite runs against its
// own database, provisioned once, never the shared flagfish_test the other integration suites use.
const importerDBName = "flagfish_test_importer"

var provisionOnce sync.Once

func importerDSN(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	own := *u
	own.Path = "/" + importerDBName
	return own.String()
}

// provision creates and migrates the importer's own database, so the suite is hermetic in CI where
// only flagfish_test exists.
func provision(t *testing.T) {
	t.Helper()
	provisionOnce.Do(func() {
		ctx := context.Background()
		admin, err := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Fatalf("connect admin: %v", err)
		}
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+importerDBName); err != nil {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42P04" { // duplicate_database
				admin.Close(ctx)
				t.Fatalf("create database: %v", err)
			}
		}
		admin.Close(ctx)

		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		if err := migrate.Run(ctx, importerDSN(t), log); err != nil {
			t.Fatalf("migrate importer db: %v", err)
		}
	})
}

func importerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	provision(t)
	pool, err := pgxpool.New(context.Background(), importerDSN(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestImportGolden runs the reference archive end to end and diffs both the resulting database
// projection and the import report against checked-in golden files. A change that silently drops a
// row or reclassifies a table fails here.
func TestImportGolden(t *testing.T) {
	pool := importerPool(t)
	ctx := context.Background()

	path := writeArchive(t, referenceArchive(t))
	report, err := importer.Run(ctx, pool, path, importer.Options{Version: "test"})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	blankReportTimes(report)
	assertGolden(t, "report.golden.json", mustIndent(t, report))

	state := projectState(t, ctx, pool)
	assertGolden(t, "state.golden.json", state)
}

// TestImportIsIdempotent proves the restore is repeatable: importing the same archive twice leaves
// the identical instance, because the restore is whole-instance and truncates before it loads.
func TestImportIsIdempotent(t *testing.T) {
	pool := importerPool(t)
	ctx := context.Background()
	path := writeArchive(t, referenceArchive(t))

	if _, err := importer.Run(ctx, pool, path, importer.Options{Version: "test"}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	first := projectState(t, ctx, pool)
	if _, err := importer.Run(ctx, pool, path, importer.Options{Version: "test"}); err != nil {
		t.Fatalf("second import: %v", err)
	}
	second := projectState(t, ctx, pool)
	if !bytes.Equal(first, second) {
		t.Fatalf("re-import changed state:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestConflictingArchiveIsAtomic proves a conflicting archive fails whole and leaves the prior
// instance byte-identical — the property the whole one-transaction design exists for.
func TestConflictingArchiveIsAtomic(t *testing.T) {
	pool := importerPool(t)
	ctx := context.Background()

	// Establish a known good instance.
	if _, err := importer.Run(ctx, pool, writeArchive(t, referenceArchive(t)), importer.Options{Version: "test"}); err != nil {
		t.Fatalf("baseline import: %v", err)
	}
	before := projectState(t, ctx, pool)

	// Two users collide on lower(email): the unique index aborts the COPY.
	bad := referenceArchive(t)
	bad["users"] = []any{
		ctfdUserRow(1, "admin", "dup@x.ctf", "admin"),
		ctfdUserRow(2, "alice", "DUP@x.ctf", "user"),
	}
	_, err := importer.Run(ctx, pool, writeArchive(t, bad), importer.Options{Version: "test"})
	if err == nil {
		t.Fatal("conflicting archive should have failed")
	}
	after := projectState(t, ctx, pool)
	if !bytes.Equal(before, after) {
		t.Fatalf("failed import mutated the instance:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMalformedArchivesRejected(t *testing.T) {
	pool := importerPool(t)
	ctx := context.Background()

	cases := map[string]func(a archiveTables){
		"unknown revision": func(a archiveTables) {
			a["alembic_version"] = []any{map[string]any{"version_num": "deadbeefcafe"}}
		},
		"rejected 1.x revision": func(a archiveTables) {
			a["alembic_version"] = []any{map[string]any{"version_num": "e62fd69bd417"}}
		},
		"unknown flag type": func(a archiveTables) {
			a["flags"] = []any{map[string]any{"id": 1, "challenge_id": 1, "type": "hashy", "content": "x"}}
		},
		"corrupt solve": func(a archiveTables) {
			// a correct submission with no matching solve row
			a["solves"] = []any{}
		},
		"bad regex flag": func(a archiveTables) {
			a["flags"] = []any{map[string]any{"id": 9, "challenge_id": 2, "type": "regex", "content": "flag{("}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			a := referenceArchive(t)
			mutate(a)
			_, err := importer.Run(ctx, pool, writeArchive(t, a), importer.Options{Version: "test"})
			if err == nil {
				t.Fatalf("%s: expected a hard error, got nil", name)
			}
		})
	}
}

func TestZipSlipRejected(t *testing.T) {
	pool := importerPool(t)
	ctx := context.Background()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// A path-traversal member alongside otherwise valid content.
	writeZipEntry(t, zw, "../escape.json", []byte("{}"))
	body, err := json.Marshal(envelope([]any{map[string]any{"version_num": "48d8250d19bd"}}))
	if err != nil {
		t.Fatal(err)
	}
	writeZipEntry(t, zw, "db/alembic_version.json", body)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "slip.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Run(ctx, pool, path, importer.Options{}); err == nil {
		t.Fatal("zip-slip archive should be rejected")
	}
}
