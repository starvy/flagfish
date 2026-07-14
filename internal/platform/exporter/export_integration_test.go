//go:build integration

package exporter_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/migrate"
	"github.com/starvy/flagfish/internal/platform/exporter"
	"github.com/starvy/flagfish/internal/platform/importer"
	"github.com/starvy/flagfish/internal/storage"
)

// The export/restore round-trip truncates the whole instance, so the suite runs against its own
// database, provisioned once, never the shared flagfish_test the other suites use.
const exportDBName = "flagfish_test_exportv2"

var provisionOnce sync.Once

func dsn(t *testing.T) string {
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
	own.Path = "/" + exportDBName
	return own.String()
}

func provision(t *testing.T) {
	t.Helper()
	provisionOnce.Do(func() {
		ctx := context.Background()
		admin, err := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Fatalf("connect admin: %v", err)
		}
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+exportDBName); err != nil {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42P04" { // duplicate_database
				admin.Close(ctx)
				t.Fatalf("create database: %v", err)
			}
		}
		admin.Close(ctx)

		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		if err := migrate.Run(ctx, dsn(t), log); err != nil {
			t.Fatalf("migrate export db: %v", err)
		}
	})
}

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	provision(t)
	p, err := pgxpool.New(context.Background(), dsn(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// testStore wires the MinIO the files feature added to the test infra.
func testStore(t *testing.T) storage.Store {
	t.Helper()
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("TEST_S3_ENDPOINT is not set — run `task test-integration` (brings up minio-test)")
	}
	cfg := storage.S3Config{
		Endpoint:  ep,
		Bucket:    envOr("TEST_S3_BUCKET", "flagfish-test"),
		Region:    "us-east-1",
		AccessKey: envOr("TEST_S3_ACCESS_KEY", "flagfishtest"),
		SecretKey: envOr("TEST_S3_SECRET_KEY", "flagfishtest123"),
		UseSSL:    false,
		PathStyle: true,
	}
	ctx := context.Background()
	var berr error
	for range 40 {
		if berr = storage.EnsureBucket(ctx, cfg); berr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if berr != nil {
		t.Fatalf("object storage never became ready: %v", berr)
	}
	store, err := storage.NewS3Store(cfg)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Distinctive markers so the masked-export test can grep the raw archive bytes for a leak.
const (
	userHashMarker  = "argon2id$USER_SEEDHASH_MARKER"
	teamHashMarker  = "argon2id$TEAM_SEEDHASH_MARKER"
	flagMarker      = "flag{SUPER_SECRET_MARKER}"
	configSecretVal = "smtp-password-MARKER"
)

var blobBytes = []byte("challenge-attachment-contents-MARKER")

func blobSHA() string {
	s := sha256.Sum256(blobBytes)
	return hex.EncodeToString(s[:])
}

// seed installs a representative instance under the replica role so no audit/cap triggers fire and
// the state is deterministic. It exercises every mask: password hashes, a flag secret, a value hash,
// a token hash, a secret config value.
func seed(t *testing.T, ctx context.Context, p *pgxpool.Pool, store storage.Store) {
	t.Helper()
	sha := blobSHA()
	if err := store.Put(ctx, sha, int64(len(blobBytes)), bytes.NewReader(blobBytes)); err != nil {
		t.Fatalf("put blob: %v", err)
	}
	vhash := sha256.Sum256([]byte("value-hash"))
	tokHash := sha256.Sum256([]byte("token"))

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer tx.Rollback(ctx)
	// Suppress triggers so seeding is a clean, deterministic state with no audit rows.
	exec := func(sql string, args ...any) {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	exec(`SET LOCAL session_replication_role = replica`)
	exec(`TRUNCATE instance, config, brackets, teams, users, fields, field_entries, api_tokens,
		tracking, challenges, files, tags, flags, hints, challenge_instances, flag_issues,
		submissions, solves, awards, hint_unlocks, notifications, audit_log, sessions,
		email_tokens, rate_limits RESTART IDENTITY CASCADE`)

	exec(`INSERT INTO instance (id, user_mode, version) VALUES (true, 'teams', 'seed')`)
	exec(`INSERT INTO config (id, key, value) VALUES (1,'user_mode','teams'), (2,'mail_password',$1)`, configSecretVal)
	exec(`INSERT INTO brackets (id, name, applies_to) VALUES (1,'open','teams')`)
	exec(`INSERT INTO teams (id, name, email, password_hash, secret, bracket_id, captain_id)
		VALUES (1,'redteam','team@x.ctf',$1,'TEAM_SECRET_MARKER',1,1)`, teamHashMarker)
	exec(`INSERT INTO users (id, name, email, password_hash, role, secret, team_id, bracket_id, verified)
		VALUES (1,'alice','alice@x.ctf',$1,'admin','USER_SECRET_MARKER',1,1,true)`, userHashMarker)
	exec(`INSERT INTO fields (id, name, applies_to, field_type) VALUES (1,'school','user','text')`)
	exec(`INSERT INTO field_entries (id, field_id, user_id, value) VALUES (1,1,1,'"MIT"'::jsonb)`)
	exec(`INSERT INTO challenges (id, name, category, description, type, state, value)
		VALUES (1,'chal','web','desc','standard','visible',100)`)
	exec(`INSERT INTO files (id, location, sha256sum, size_bytes, challenge_id, name)
		VALUES (1,'chal/f.bin', decode($1,'hex'), $2, 1, 'f.bin')`, sha, len(blobBytes))
	exec(`INSERT INTO tags (id, challenge_id, value) VALUES (1,1,'pwn')`)
	exec(`INSERT INTO flags (id, challenge_id, type, content) VALUES (1,1,'static',$1)`, flagMarker)
	exec(`INSERT INTO hints (id, challenge_id, content, cost) VALUES (1,1,'try harder',10)`)
	exec(`INSERT INTO challenge_instances (id, challenge_id, value_hash, artifact_id)
		VALUES (1,1,$1,1)`, vhash[:])
	exec(`INSERT INTO flag_issues (challenge_id, account_id, instance_id) VALUES (1,1,1)`)
	exec(`INSERT INTO submissions (id, challenge_id, user_id, team_id, type, provided, ip, attributed_account_id)
		VALUES (1,1,1,1,'correct',$1,'203.0.113.9',1)`, flagMarker)
	exec(`INSERT INTO solves (id, submission_id, challenge_id, user_id, team_id, value)
		VALUES (1,1,1,1,1,100)`)
	exec(`INSERT INTO awards (id, user_id, team_id, type, name, value) VALUES (1,1,1,'standard','manual',5)`)
	exec(`INSERT INTO hint_unlocks (id, hint_id, user_id, team_id, award_id) VALUES (1,1,1,1,1)`)
	exec(`INSERT INTO tracking (id, user_id, ip) VALUES (1,1,'203.0.113.9')`)
	exec(`INSERT INTO notifications (id, title, content) VALUES (1,'welcome','hi')`)
	// Excluded-table rows: prove they never leave and are gone after restore.
	exec(`INSERT INTO api_tokens (id, user_id, token_hash, expires_at)
		VALUES (1,1,$1, now() + interval '30 days')`, tokHash[:])
	exec(`INSERT INTO sessions (id_hash, user_id, pw_fingerprint, csrf_token, expires_at)
		VALUES (decode('aa','hex'),1,decode('bb','hex'),'csrf', now() + interval '1 day')`)

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

// projectionTables mirrors the exporter's registry: the tables a backup restores. Excluded tables
// (api_tokens, sessions, …) are intentionally absent — a backup drops them by design.
var projectionTables = []struct{ name, order string }{
	{"instance", "id"},
	{"config", "id"},
	{"brackets", "id"},
	{"teams", "id"},
	{"users", "id"},
	{"fields", "id"},
	{"field_entries", "id"},
	{"challenges", "id"},
	{"files", "id"},
	{"tags", "id"},
	{"flags", "id"},
	{"hints", "id"},
	{"challenge_instances", "id"},
	{"flag_issues", "challenge_id, account_id"},
	{"submissions", "id"},
	{"solves", "id"},
	{"awards", "id"},
	{"hint_unlocks", "id"},
	{"tracking", "id"},
	{"notifications", "id"},
	{"audit_log", "id"},
}

// projectState renders every restorable table as canonical JSON, so two states can be byte-compared.
func projectState(t *testing.T, ctx context.Context, p *pgxpool.Pool) []byte {
	t.Helper()
	out := map[string]json.RawMessage{}
	for _, tbl := range projectionTables {
		var agg []byte
		q := fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY %s), '[]'::jsonb) FROM %s x`, tbl.order, tbl.name)
		if err := p.QueryRow(ctx, q).Scan(&agg); err != nil {
			t.Fatalf("project %s: %v", tbl.name, err)
		}
		out[tbl.name] = agg
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal projection: %v", err)
	}
	return b
}

// TestBackupRoundTrip seeds an instance, exports a full backup, deletes a blob and wipes the DB, then
// restores and asserts the DB projection (hashes, flags, blobs) is byte-identical to the original.
func TestBackupRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p, store)

	before := projectState(t, ctx, p)

	var buf bytes.Buffer
	rep, err := exporter.Export(ctx, p, store, exporter.ProfileBackup, "test", &buf)
	if err != nil {
		t.Fatalf("export backup: %v", err)
	}
	if rep.Files != 1 {
		t.Fatalf("expected 1 exported file, got %d", rep.Files)
	}

	// Prove the restore rehydrates the blob: remove it, then require it back afterwards.
	if err := store.Delete(ctx, blobSHA()); err != nil {
		t.Fatalf("delete blob: %v", err)
	}

	rdr := bytes.NewReader(buf.Bytes())
	if _, err := exporter.Restore(ctx, p, store, rdr, int64(buf.Len())); err != nil {
		t.Fatalf("restore backup: %v", err)
	}

	after := projectState(t, ctx, p)
	if !bytes.Equal(before, after) {
		t.Fatalf("round-trip changed state:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	// The blob is back...
	if ok, err := store.Stat(ctx, blobSHA()); err != nil || !ok {
		t.Fatalf("blob not rehydrated by restore (ok=%v err=%v)", ok, err)
	}
	// ...and the excluded tables are empty, as a backup drops them by design.
	var tokens int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM api_tokens`).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 0 {
		t.Fatalf("api_tokens should be empty after restore, got %d", tokens)
	}
}

// TestSafeExportIsMasked asserts a safe archive leaks no secret in its bytes and cannot be restored.
func TestSafeExportIsMasked(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p, store)

	var buf bytes.Buffer
	rep, err := exporter.Export(ctx, p, store, exporter.ProfileSafe, "test", &buf)
	if err != nil {
		t.Fatalf("export safe: %v", err)
	}
	if rep.Profile != exporter.ProfileSafe {
		t.Fatalf("profile = %q", rep.Profile)
	}

	// The archive is a zip; secrets could hide compressed. Grep the concatenation of every member's
	// decompressed bytes, not the zip container.
	plain := decompressAll(t, buf.Bytes())
	for _, secret := range []string{
		userHashMarker, teamHashMarker, "USER_SECRET_MARKER", "TEAM_SECRET_MARKER",
		flagMarker, configSecretVal,
	} {
		if bytes.Contains(plain, []byte(secret)) {
			t.Fatalf("safe archive leaked a secret: %q", secret)
		}
	}
	// The value hash (sha256 of the flag surrogate) must not appear either.
	vhash := sha256.Sum256([]byte("value-hash"))
	if bytes.Contains(plain, []byte(hex.EncodeToString(vhash[:]))) {
		t.Fatal("safe archive leaked a challenge-instance value hash")
	}

	// The manifest must declare the profile and the mask.
	m := readManifest(t, buf.Bytes())
	if m.Profile != exporter.ProfileSafe {
		t.Fatalf("manifest profile = %q", m.Profile)
	}
	if len(m.Omitted) == 0 {
		t.Fatal("manifest.omitted is empty on a safe export")
	}

	// A safe archive is not restorable.
	rdr := bytes.NewReader(buf.Bytes())
	_, err = exporter.Restore(ctx, p, store, rdr, int64(buf.Len()))
	if err == nil {
		t.Fatal("restoring a safe archive should be rejected")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("safe")) {
		t.Fatalf("rejection should name the profile, got: %v", err)
	}
}

// TestNotForeignCompatible asserts the archive is our own shape, and that the importer we use for
// foreign archives refuses it — the two formats are not interchangeable.
func TestNotForeignCompatible(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p, store)

	var buf bytes.Buffer
	if _, err := exporter.Export(ctx, p, store, exporter.ProfileBackup, "test", &buf); err != nil {
		t.Fatalf("export: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	var hasManifest, hasData bool
	for _, f := range zr.File {
		switch f.Name {
		case "manifest.json":
			hasManifest = true
		case "data/users.jsonl":
			hasData = true
		}
		// The foreign import layout is db/<table>.json + db/alembic_version.json. We must emit neither.
		if strings.HasPrefix(f.Name, "db/") {
			t.Fatalf("archive contains a foreign-format member %q", f.Name)
		}
	}
	if !hasManifest || !hasData {
		t.Fatalf("archive is not our shape (manifest=%v data=%v)", hasManifest, hasData)
	}

	m := readManifest(t, buf.Bytes())
	if m.Format != "flagfish/export" {
		t.Fatalf("manifest.format = %q, want flagfish/export", m.Format)
	}

	// The foreign-archive importer must reject our archive outright.
	path := filepath.Join(t.TempDir(), "ours.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Run(ctx, p, path, importer.Options{Version: "test"}); err == nil {
		t.Fatal("the foreign-archive importer accepted our own archive")
	}
}

// TestWrongSchemaVersionRejected proves a schema pin mismatch is a hard error, not a silent restore.
func TestWrongSchemaVersionRejected(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p, store)

	var buf bytes.Buffer
	if _, err := exporter.Export(ctx, p, store, exporter.ProfileBackup, "test", &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	tampered := rewriteManifest(t, buf.Bytes(), func(m *exporter.Manifest) { m.SchemaVersion = 999999 })
	_, err := exporter.Restore(ctx, p, store, bytes.NewReader(tampered), int64(len(tampered)))
	if err == nil {
		t.Fatal("a schema-version mismatch should be rejected")
	}
}

// --- archive test helpers ---

func decompressAll(t *testing.T, zipBytes []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	var all bytes.Buffer
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open member %s: %v", f.Name, err)
		}
		if _, err := io.Copy(&all, rc); err != nil {
			rc.Close()
			t.Fatalf("read member %s: %v", f.Name, err)
		}
		rc.Close()
	}
	return all.Bytes()
}

func readManifest(t *testing.T, zipBytes []byte) exporter.Manifest {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var m exporter.Manifest
		decErr := json.NewDecoder(rc).Decode(&m)
		rc.Close()
		if decErr != nil {
			t.Fatalf("decode manifest: %v", decErr)
		}
		return m
	}
	t.Fatal("no manifest.json in archive")
	return exporter.Manifest{}
}

// rewriteManifest rebuilds the zip with a mutated manifest, so a tampered-archive path can be tested.
func rewriteManifest(t *testing.T, zipBytes []byte, mutate func(*exporter.Manifest)) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if f.Name == "manifest.json" {
			var m exporter.Manifest
			if uerr := json.Unmarshal(body, &m); uerr != nil {
				t.Fatal(uerr)
			}
			mutate(&m)
			marshaled, merr := json.MarshalIndent(m, "", "  ")
			if merr != nil {
				t.Fatal(merr)
			}
			body = marshaled
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
