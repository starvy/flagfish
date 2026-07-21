//go:build integration

package exporter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/platform/exporter"
)

// tear is one way a live event can tear an archive: a parent row and its child committed together in
// the window between the export reading the parent table and reading the child table. Read table by
// table at READ COMMITTED, the child lands in the archive and the parent does not — the archive then
// fails to restore, which is only discovered on the day it is needed.
type tear struct {
	name string
	// firesBefore is the registry table the export is about to read when the write commits, so every
	// table listed before it has already been read.
	firesBefore string
	write       []string
	// newIDs are the ids written mid-export, per table: absent from a point-in-time archive.
	newIDs map[string]float64
	// dangling is the reference that a torn archive leaves unresolvable.
	dangling struct{ child, col, parent string }
}

func tears() []tear {
	mk := func(name, firesBefore string, write []string, ids map[string]float64, child, col, parent string) tear {
		tr := tear{name: name, firesBefore: firesBefore, write: write, newIDs: ids}
		tr.dangling.child, tr.dangling.col, tr.dangling.parent = child, col, parent
		return tr
	}
	return []tear{
		mk("challenge published then attempted", "submissions",
			[]string{
				`INSERT INTO challenges (id, name, category, description, type, state, value)
					VALUES (900,'late-chal','web','desc','standard','visible',100)`,
				`INSERT INTO submissions (id, challenge_id, user_id, team_id, type, provided, attributed_account_id)
					VALUES (900,900,1,1,'incorrect','nope',1)`,
			},
			map[string]float64{"challenges": 900, "submissions": 900},
			"submissions", "challenge_id", "challenges"),

		mk("challenge solved mid-export", "solves",
			[]string{
				`INSERT INTO challenges (id, name, category, description, type, state, value)
					VALUES (901,'solved-chal','web','desc','standard','visible',100)`,
				`INSERT INTO submissions (id, challenge_id, user_id, team_id, type, provided, attributed_account_id)
					VALUES (901,901,1,1,'correct','flag{x}',1)`,
				`INSERT INTO solves (id, submission_id, challenge_id, user_id, team_id, value)
					VALUES (901,901,901,1,1,100)`,
			},
			map[string]float64{"challenges": 901, "submissions": 901, "solves": 901},
			"solves", "submission_id", "submissions"),

		mk("hint bought mid-export", "hint_unlocks",
			[]string{
				`INSERT INTO hints (id, challenge_id, content, cost) VALUES (902,1,'late hint',10)`,
				`INSERT INTO awards (id, user_id, team_id, type, name, value) VALUES (902,1,1,'standard','hint',-10)`,
				`INSERT INTO hint_unlocks (id, hint_id, user_id, team_id, award_id) VALUES (902,902,1,1,902)`,
			},
			map[string]float64{"hints": 902, "awards": 902, "hint_unlocks": 902},
			"hint_unlocks", "hint_id", "hints"),
	}
}

// TestExportIsASingleSnapshot proves the export reads one instant. Each case commits a parent and its
// child on a second connection at a controlled point inside a running export, then requires the
// archive to be internally consistent and to restore. Without the snapshot transaction every case
// fails: the child is exported, its parent is not, and the restore rejects the dangling reference.
func TestExportIsASingleSnapshot(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)

	for _, tc := range tears() {
		t.Run(tc.name, func(t *testing.T) {
			seed(t, ctx, p, store)

			fired := false
			hook := func(ctx context.Context, detail string, _ int) {
				if fired || detail != "exporting "+tc.firesBefore {
					return
				}
				fired = true
				commitDuringExport(t, ctx, p, tc.write)
			}

			var buf bytes.Buffer
			if _, err := exporter.ExportWithProgress(ctx, p, store, exporter.ProfileBackup, "test", &buf, hook); err != nil {
				t.Fatalf("export: %v", err)
			}
			if !fired {
				t.Fatalf("interleaving never fired: no progress report for %q", tc.firesBefore)
			}
			archive := buf.Bytes()

			// The write landed after the snapshot, so no table in the archive may carry any of it —
			// that is what makes the set of tables one instant rather than N.
			for table, id := range tc.newIDs {
				if hasID(t, archive, table, id) {
					t.Errorf("archive carries %s id=%v written after the snapshot: the export is not point-in-time",
						table, id)
				}
			}

			d := tc.dangling
			children := idSet(t, archive, d.child, d.col)
			parents := idSet(t, archive, d.parent, "id")
			for ref := range children {
				if !parents[ref] {
					t.Errorf("archive is torn: %s.%s=%v has no row in %s", d.child, d.col, ref, d.parent)
				}
			}

			// The real gate: restore re-validates every foreign key and refuses a torn archive.
			if _, err := exporter.Restore(ctx, p, store, bytes.NewReader(archive), int64(len(archive))); err != nil {
				t.Fatalf("archive taken during concurrent writes does not restore: %v", err)
			}
		})
	}
}

// commitDuringExport applies the statements as one transaction on its own connection, exactly as an
// event's own traffic would while an export runs. Triggers are suppressed so the write is only what
// the case names.
func commitDuringExport(t *testing.T, ctx context.Context, p *pgxpool.Pool, stmts []string) {
	t.Helper()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin concurrent write: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // the commit below is the operative path
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("concurrent write, replica role: %v", err)
	}
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("concurrent write %q: %v", s, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent write: %v", err)
	}
}

// idSet collects the non-null numeric values of one column of one archive table.
func idSet(t *testing.T, archive []byte, table, col string) map[float64]bool {
	t.Helper()
	out := map[float64]bool{}
	for _, row := range jsonlRows(t, archive, "data/"+table+".jsonl") {
		raw, ok := row[col]
		if !ok || string(raw) == "null" {
			continue
		}
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("decode %s.%s: %v", table, col, err)
		}
		out[v] = true
	}
	return out
}

func hasID(t *testing.T, archive []byte, table string, id float64) bool {
	t.Helper()
	return idSet(t, archive, table, "id")[id]
}
