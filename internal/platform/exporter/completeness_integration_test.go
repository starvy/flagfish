//go:build integration

package exporter

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestMaskCompleteness proves every table at schema head is a deliberate decision: either in the
// export registry or on the excluded list. A new migration that adds a table fails here until someone
// classifies it — so the failure mode is "a table is unclassified", never "a secret silently leaked".
func TestMaskCompleteness(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	known := map[string]bool{}
	for _, t := range registry {
		known[t.name] = true
	}
	for _, name := range excludedTables {
		known[name] = true
	}

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		// Not ours: goose's bookkeeping and River's own tables.
		if name == "goose_db_version" || strings.HasPrefix(name, "river") {
			continue
		}
		if !known[name] {
			t.Errorf("table %q is at schema head but not in the export registry or the excluded list: "+
				"classify it (export with a mask, or exclude it)", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
