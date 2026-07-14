//go:build integration

package importer_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/platform/importer"
)

type archiveTables map[string][]any

func envelope(rows []any) map[string]any {
	if rows == nil {
		rows = []any{}
	}
	return map[string]any{"count": len(rows), "results": rows, "meta": map[string]any{}}
}

// fixtureUploads is the constant upload tree the reference files table points at, plus one orphan
// blob (referenced by no file row) to exercise the "upload without a file" report line.
var fixtureUploads = map[string][]byte{
	"a1b2c3/warmup.zip": []byte("warmup challenge bytes"),
	"d4e5f6/logo.png":   []byte("\x89PNG fixture logo"),
	"z9z9z9/orphan.txt": []byte("no file row references me"),
}

func writeZipEntry(t *testing.T, zw *zip.Writer, name string, data []byte) {
	t.Helper()
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
}

func writeArchive(t *testing.T, tables archiveTables) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, rows := range tables {
		body, err := json.Marshal(envelope(rows))
		if err != nil {
			t.Fatal(err)
		}
		writeZipEntry(t, zw, "db/"+name+".json", body)
	}
	for loc, data := range fixtureUploads {
		writeZipEntry(t, zw, "uploads/"+loc, data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const bcryptHash = "$2b$12$0123456789012345678901uReTUYbXNq9m0Zx1c2v3B4n5M6k7J8i" // fixed, opaque to the importer

func ctfdUserRow(id int, name, email, typ string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "email": email, "password": bcryptHash, "type": typ,
		"verified": true, "hidden": false, "banned": false,
	}
}

// referenceArchive is the reference export: static + regex + case-insensitive flags, a dynamic
// challenge whose scoring lives in dynamic_challenge, hints with and without a title, tags (including
// a duplicate to fold), challenge and standard files, users with an admin, a team with a captain,
// correct and incorrect submissions, value-stamped solves, positive and negative awards, config with
// a duplicate key and an unmodelled key, plus deferred, dropped, and unmapped tables.
func referenceArchive(t *testing.T) archiveTables {
	t.Helper()
	return archiveTables{
		"alembic_version": {map[string]any{"version_num": "48d8250d19bd"}},

		"config": {
			map[string]any{"id": 1, "key": "ctf_name", "value": "OLD"},
			map[string]any{"id": 50, "key": "ctf_name", "value": "Fixture CTF"},
			map[string]any{"id": 2, "key": "ctf_description", "value": "a fixture event"},
			map[string]any{"id": 3, "key": "user_mode", "value": "users"},
			map[string]any{"id": 4, "key": "challenge_visibility", "value": "private"},
			map[string]any{"id": 5, "key": "score_visibility", "value": "public"},
			map[string]any{"id": 6, "key": "account_visibility", "value": "public"},
			map[string]any{"id": 7, "key": "registration_visibility", "value": "public"},
			map[string]any{"id": 8, "key": "paused", "value": "false"},
			map[string]any{"id": 9, "key": "ctf_version", "value": "3.8.6"},
			map[string]any{"id": 10, "key": "custom_plugin_setting", "value": "kept-verbatim"},
		},

		"brackets": {map[string]any{"id": 1, "name": "Open", "description": nil, "type": "users"}},

		"users": {
			ctfdUserRow(1, "admin", "admin@x.ctf", "admin"),
			mergeMap(ctfdUserRow(2, "alice", "alice@x.ctf", "user"), map[string]any{"team_id": 1, "bracket_id": 1}),
			mergeMap(ctfdUserRow(3, "bob", "bob@x.ctf", "user"), map[string]any{"team_id": 1}),
		},

		"teams": {
			map[string]any{
				"id": 1, "name": "Team One", "email": "team1@x.ctf", "password": bcryptHash,
				"captain_id": 2, "bracket_id": 1, "hidden": false, "banned": false,
				"secret": "should-be-dropped",
			},
		},

		"challenges": {
			map[string]any{
				"id": 1, "name": "Warmup", "category": "misc", "description": "warm up",
				"type": "standard", "value": 100, "state": "visible", "max_attempts": 0,
			},
			map[string]any{
				"id": 2, "name": "Regexy", "category": "misc", "description": "match me",
				"type": "standard", "value": 200, "state": "visible",
			},
			map[string]any{
				"id": 3, "name": "Decayer", "category": "pwn", "description": "worth less over time",
				"type": "dynamic", "value": 238, "state": "visible",
				// string-encoded requirements exercise the peel; prereq is challenge 1.
				"requirements": "{\"prerequisites\": [1]}",
			},
		},

		"dynamic_challenge": {
			map[string]any{"id": 3, "initial": 500, "minimum": 100, "decay": 25, "function": "logarithmic", "value": 238},
		},

		"flags": {
			map[string]any{"id": 1, "challenge_id": 1, "type": "static", "content": "flag{warmup}", "data": ""},
			map[string]any{"id": 2, "challenge_id": 1, "type": "static", "content": "flag{WARMUP2}", "data": "case_insensitive"},
			map[string]any{"id": 3, "challenge_id": 2, "type": "regex", "content": "flag\\{.*\\}", "data": "case_insensitive"},
			map[string]any{"id": 4, "challenge_id": 3, "type": "static", "content": "flag{decay}", "data": ""},
		},

		"tags": {
			map[string]any{"id": 1, "challenge_id": 1, "value": "beginner"},
			map[string]any{"id": 2, "challenge_id": 3, "value": "pwn"},
			map[string]any{"id": 3, "challenge_id": 1, "value": "beginner"}, // duplicate, folded
		},

		"hints": {
			map[string]any{"id": 1, "challenge_id": 3, "title": "Nudge", "content": "look closer", "cost": 50},
			map[string]any{"id": 2, "challenge_id": 3, "title": nil, "content": "look even closer", "cost": 0},
		},

		"files": {
			map[string]any{"id": 1, "type": "challenge", "location": "a1b2c3/warmup.zip", "challenge_id": 1},
			map[string]any{"id": 2, "type": "standard", "location": "d4e5f6/logo.png"},
		},

		"submissions": {
			map[string]any{"id": 1, "challenge_id": 1, "user_id": 2, "team_id": 1, "type": "correct", "provided": "flag{warmup}", "ip": "10.0.0.5", "date": "2024-01-01T10:00:00+00:00"},
			map[string]any{"id": 2, "challenge_id": 1, "user_id": 3, "team_id": 1, "type": "incorrect", "provided": "nope", "ip": "10.0.0.6", "date": "2024-01-01T10:05:00+00:00"},
			map[string]any{"id": 3, "challenge_id": 3, "user_id": 2, "team_id": 1, "type": "correct", "provided": "flag{decay}", "ip": "10.0.0.5", "date": "2024-01-01T11:00:00+00:00"},
		},

		"solves": {
			map[string]any{"id": 1, "challenge_id": 1, "user_id": 2, "team_id": 1},
			map[string]any{"id": 3, "challenge_id": 3, "user_id": 2, "team_id": 1},
		},

		"awards": {
			map[string]any{"id": 1, "user_id": 2, "team_id": 1, "type": "standard", "name": "Bonus", "value": 10, "date": "2024-01-01T12:00:00+00:00"},
			map[string]any{"id": 2, "user_id": 2, "team_id": 1, "type": "standard", "name": "Hint spend", "value": -50, "date": "2024-01-01T11:30:00+00:00"},
		},

		// Never imported: plaintext credentials.
		"tokens": {map[string]any{"id": 1, "user_id": 1, "value": "ctfd_deadbeef", "type": "user"}},
		// Deferred subsystems: counted, named, dropped.
		"notifications": {map[string]any{"id": 1, "title": "Welcome", "content": "gl hf", "date": "2024-01-01T09:00:00+00:00"}},
		"tracking":      {map[string]any{"id": 1, "user_id": 2, "ip": "10.0.0.5", "date": "2024-01-01T09:30:00+00:00"}},
		// No counterpart at all: a plugin table.
		"custom_plugin_data": {map[string]any{"id": 1, "blob": "opaque"}},
	}
}

func mergeMap(base, extra map[string]any) map[string]any {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

// ---- golden + projection ----

// blankReportTimes zeroes the wall-clock timestamps so the report golden is stable.
func blankReportTimes(r *importer.Report) {
	r.StartedAt = time.Time{}
	r.EndedAt = time.Time{}
}

func projectState(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []byte {
	t.Helper()
	// Deterministic columns only: created_at/updated_at are wall-clock and excluded; archive-sourced
	// dates are kept, rendered in UTC. Hashes are hex-encoded.
	queries := []struct{ name, sql string }{
		{"instance", `SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]')::text FROM (SELECT user_mode, version FROM instance) t`},
		{"config", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.key),'[]')::text FROM (SELECT key, value FROM config) t`},
		{"brackets", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, name, description, applies_to FROM brackets) t`},
		{"teams", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, name, email, password_hash, secret, captain_id, bracket_id, hidden, banned FROM teams) t`},
		{"users", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, name, email, password_hash, secret, role, team_id, bracket_id, hidden, banned, verified, must_change_password FROM users) t`},
		{"challenges", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, name, category, type, state, value, function, initial, minimum, decay, max_attempts, logic, flag_mode, first_blood, requirements FROM challenges) t`},
		{"files", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, location, encode(sha256sum,'hex') AS sha256, size_bytes, challenge_id FROM files) t`},
		{"flags", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, challenge_id, type, content, case_insensitive FROM flags) t`},
		{"tags", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, challenge_id, value FROM tags) t`},
		{"hints", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, challenge_id, title, content, cost, position FROM hints) t`},
		{"submissions", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, challenge_id, user_id, team_id, type, provided, host(ip) AS ip, to_char(date AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS') AS date, attributed_account_id FROM submissions) t`},
		{"solves", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, submission_id, challenge_id, user_id, team_id, value, to_char(date AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS') AS date FROM solves) t`},
		{"awards", `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]')::text FROM (SELECT id, user_id, team_id, type, challenge_id, name, value, to_char(date AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS') AS date FROM awards) t`},
	}
	out := map[string]json.RawMessage{}
	for _, q := range queries {
		var text string
		if err := pool.QueryRow(ctx, q.sql).Scan(&text); err != nil {
			t.Fatalf("project %s: %v", q.name, err)
		}
		out[q.name] = json.RawMessage(text)
	}
	return mustIndent(t, out)
}

func mustIndent(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated golden %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with UPDATE_GOLDEN=1 to create): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("golden %s mismatch\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}
