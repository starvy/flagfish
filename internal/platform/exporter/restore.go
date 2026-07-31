package exporter

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/storage"
)

// restoreLockKey serialises restores against each other, belt-and-braces over the single-in-flight
// task guard. Arbitrary but fixed.
const restoreLockKey int64 = 0x7265737472 // "restr"

// maxMemberBytes caps a single archive member; maxTotalBytes caps the whole uncompressed size. A
// crafted zip must not exhaust memory before we have read a byte of data.
const (
	maxMemberBytes = 1 << 30 // 1 GiB
	maxTotalBytes  = 8 << 30 // 8 GiB
)

// RestoreReport summarises a restore: what came back and how many blobs it rehydrated.
type RestoreReport struct {
	Profile     Profile
	UserMode    string
	Rows        map[string]int
	Files       int
	UploadBytes int64
}

// fk is one referential check the restore re-validates after loading. The insert runs with foreign
// keys suppressed so ids and order are ours to control; re-enabling the role does not re-check the
// rows already there, so a dangling reference is caught here explicitly.
type fk struct {
	child, childCol, parent, parentCol string
}

var foreignKeys = []fk{
	{"teams", "bracket_id", "brackets", "id"},
	{"teams", "captain_id", "users", "id"},
	{"users", "bracket_id", "brackets", "id"},
	{"users", "team_id", "teams", "id"},
	{"field_entries", "field_id", "fields", "id"},
	{"field_entries", "user_id", "users", "id"},
	{"field_entries", "team_id", "teams", "id"},
	{"files", "challenge_id", "challenges", "id"},
	{"tags", "challenge_id", "challenges", "id"},
	{"challenge_annotations", "challenge_id", "challenges", "id"},
	{"flags", "challenge_id", "challenges", "id"},
	{"hints", "challenge_id", "challenges", "id"},
	{"challenge_instances", "challenge_id", "challenges", "id"},
	{"challenge_instances", "artifact_id", "files", "id"},
	{"flag_issues", "challenge_id", "challenges", "id"},
	{"flag_issues", "instance_id", "challenge_instances", "id"},
	{"submissions", "challenge_id", "challenges", "id"},
	{"submissions", "user_id", "users", "id"},
	{"submissions", "team_id", "teams", "id"},
	{"solves", "submission_id", "submissions", "id"},
	{"solves", "challenge_id", "challenges", "id"},
	{"solves", "user_id", "users", "id"},
	{"solves", "team_id", "teams", "id"},
	{"awards", "user_id", "users", "id"},
	{"awards", "team_id", "teams", "id"},
	{"awards", "challenge_id", "challenges", "id"},
	{"hint_unlocks", "hint_id", "hints", "id"},
	{"hint_unlocks", "user_id", "users", "id"},
	{"hint_unlocks", "team_id", "teams", "id"},
	{"hint_unlocks", "award_id", "awards", "id"},
}

// Restore rehydrates a backup archive into the instance behind pool. See RestoreWithProgress; this
// is the no-progress form the CLI uses.
func Restore(ctx context.Context, pool *pgxpool.Pool, store storage.Store, r io.ReaderAt, size int64) (*RestoreReport, error) {
	return RestoreWithProgress(ctx, pool, store, r, size, nil)
}

// RestoreWithProgress rehydrates a backup archive into the instance behind pool, in one transaction.
// A safe archive is rejected: it lacks the secrets a live instance needs. Any error rolls the whole
// restore back, so a failed restore leaves the instance exactly as it was. The user mode is taken
// from the archive's instance row, the authoritative source.
//
// progress, when non-nil, is called between table loads while the single transaction is still open.
// It MUST write on a connection of its own — the load's own UPDATEs to a progress row would be
// invisible until this transaction commits, which is the whole reason progress lives outside it.
func RestoreWithProgress(ctx context.Context, pool *pgxpool.Pool, store storage.Store, r io.ReaderAt, size int64, progress Progress) (*RestoreReport, error) {
	arc, err := openArchive(r, size)
	if err != nil {
		return nil, fmt.Errorf("restore: %w", err)
	}

	m := arc.manifest
	if m.Format != formatMagic {
		return nil, fmt.Errorf("restore: not our archive format (manifest.format=%q, want %q)", m.Format, formatMagic)
	}
	if m.FormatVersion != formatVersion {
		return nil, fmt.Errorf("restore: archive format version %d, this build reads %d", m.FormatVersion, formatVersion)
	}
	if m.Profile != ProfileBackup {
		return nil, fmt.Errorf("restore: this is a %q archive and cannot be restored — it was field-masked "+
			"and carries no password hashes or flags. Re-export with --backup for a restorable archive", m.Profile)
	}

	schemaVersion, err := scanInt64(ctx, pool, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`)
	if err != nil {
		return nil, fmt.Errorf("restore: read schema version: %w", err)
	}
	if schemaVersion != m.SchemaVersion {
		return nil, fmt.Errorf("restore: archive was taken at schema version %d but this instance is at %d — "+
			"migrate to the archive's version first", m.SchemaVersion, schemaVersion)
	}

	rep := &RestoreReport{Profile: m.Profile, UserMode: m.UserMode, Rows: map[string]int{}}

	// Blobs are written before the transaction: object storage is not transactional, so a rolled-back
	// restore leaves unreferenced (garbage-collectable) blobs rather than DB rows pointing at nothing.
	progress.report(ctx, "restoring file blobs", 5)
	uploadBytes, err := restoreUploads(ctx, store, arc, rep)
	if err != nil {
		return nil, err
	}
	rep.UploadBytes = uploadBytes

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("restore: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; the safety net otherwise

	// replica suppresses foreign keys and every audit/cap/guard trigger for the load: tables load in
	// any order, ids are preserved, and no audit rows are generated. Unique and check constraints stay
	// live, so a corrupt archive still fails atomically.
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		return nil, fmt.Errorf("restore: suppress triggers: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, restoreLockKey); err != nil {
		return nil, fmt.Errorf("restore: lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `TRUNCATE `+strings.Join(allTables(), ", ")+` RESTART IDENTITY CASCADE`); err != nil {
		return nil, fmt.Errorf("restore: truncate: %w", err)
	}

	for i, t := range registry {
		// Reported on progress's own connection: this transaction has not committed, so the row it
		// makes visible to a poller is the one that write puts there, not anything written here.
		progress.report(ctx, "restoring "+t.name, 10+i*80/len(registry))
		meta, ok := m.Tables[t.name]
		if !ok {
			return nil, fmt.Errorf("restore: archive manifest is missing table %q", t.name)
		}
		rows, rerr := arc.tableRows(t.name, meta)
		if rerr != nil {
			return nil, fmt.Errorf("restore: %w", rerr)
		}
		if len(rows) == 0 {
			continue
		}
		arr, jerr := jsonArray(rows)
		if jerr != nil {
			return nil, fmt.Errorf("restore: assemble %s: %w", t.name, jerr)
		}
		// jsonb_populate_recordset reconstructs each row from the archive's JSON using the table's own
		// column types, so bytea, inet, timestamptz and jsonb all decode exactly as they were dumped.
		tag, eerr := tx.Exec(ctx,
			`INSERT INTO `+t.name+` SELECT * FROM jsonb_populate_recordset(NULL::`+t.name+`, $1::jsonb)`, arr)
		if eerr != nil {
			return nil, fmt.Errorf("restore: load %s: %w", t.name, eerr)
		}
		if int(tag.RowsAffected()) != len(rows) {
			return nil, fmt.Errorf("restore: loaded %d of %d rows into %s", tag.RowsAffected(), len(rows), t.name)
		}
		rep.Rows[t.name] = len(rows)
	}

	if err := resyncSequences(ctx, tx); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = DEFAULT`); err != nil {
		return nil, fmt.Errorf("restore: restore triggers: %w", err)
	}
	if err := validateFKs(ctx, tx); err != nil {
		return nil, err
	}

	progress.report(ctx, "committing", 95)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("restore: commit: %w", err)
	}
	return rep, nil
}

// allTables is every table a restore wipes: the exported registry plus the operational tables an
// archive never carries, so the instance is genuinely empty afterwards. Compile-time, never from the
// archive. River's own tables are left untouched — and so is `tasks`: it holds the row tracking THIS
// restore. Truncating it would delete the restore's own progress record on commit, and its
// ACCESS EXCLUSIVE lock would block the progress writes that run — by design — on a second
// connection while this transaction is still open. The restore replaces game data, not the
// operational task log.
func allTables() []string {
	out := make([]string, 0, len(registry)+len(excludedTables))
	for _, t := range registry {
		out = append(out, t.name)
	}
	for _, t := range excludedTables {
		if t == "tasks" {
			continue
		}
		out = append(out, t)
	}
	return out
}

func resyncSequences(ctx context.Context, tx pgx.Tx) error {
	for _, t := range registry {
		if !t.hasSerialID {
			continue
		}
		// The identifier comes from the compile-time registry, never the archive.
		stmt := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s','id'), GREATEST(COALESCE((SELECT MAX(id) FROM %s),0)+1,1), false)`,
			t.name, t.name,
		)
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("restore: resync sequence for %s: %w", t.name, err)
		}
	}
	return nil
}

func validateFKs(ctx context.Context, tx pgx.Tx) error {
	for _, k := range foreignKeys {
		q := fmt.Sprintf(
			`SELECT count(*) FROM %s c LEFT JOIN %s p ON c.%s = p.%s WHERE c.%s IS NOT NULL AND p.%s IS NULL`,
			k.child, k.parent, k.childCol, k.parentCol, k.childCol, k.parentCol,
		)
		var n int64
		if err := tx.QueryRow(ctx, q).Scan(&n); err != nil {
			return fmt.Errorf("restore: validate %s.%s -> %s: %w", k.child, k.childCol, k.parent, err)
		}
		if n > 0 {
			return fmt.Errorf("restore: %d dangling reference(s) in %s.%s -> %s: the archive is internally inconsistent",
				n, k.child, k.childCol, k.parent)
		}
	}
	return nil
}

// jsonArray packs newline-delimited row objects into a single JSON array for jsonb_populate_recordset.
func jsonArray(rows [][]byte) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, r := range rows {
		if i > 0 {
			buf.WriteByte(',')
		}
		if !json.Valid(r) {
			return nil, fmt.Errorf("row %d is not valid JSON", i)
		}
		buf.Write(r)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

func restoreUploads(ctx context.Context, store storage.Store, arc *archive, rep *RestoreReport) (int64, error) {
	var total int64
	for sha, body := range arc.uploads {
		if got := hex.EncodeToString(sha256Sum(body)); got != sha {
			return 0, fmt.Errorf("restore: upload %q hashes to %s: archive is corrupt", sha, got)
		}
		if err := store.Put(ctx, sha, int64(len(body)), bytes.NewReader(body)); err != nil {
			return 0, fmt.Errorf("restore: write object %s: %w", sha, err)
		}
		total += int64(len(body))
		rep.Files++
	}
	return total, nil
}

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// archive is a parsed, validated restore source held in memory: the manifest, the per-table row
// bytes, and the blob tree keyed by content address.
type archive struct {
	manifest Manifest
	data     map[string][]byte // table name -> raw data/<table>.jsonl bytes
	uploads  map[string][]byte // sha256 hex -> blob bytes
}

// tableRows splits a table's data file into rows, verifying the checksum and row count the manifest
// declared. A mismatch is a hard error: a tampered or truncated archive must not restore.
func (a *archive) tableRows(name string, meta TableMeta) ([][]byte, error) {
	raw, ok := a.data[name]
	if !ok {
		return nil, fmt.Errorf("archive is missing data/%s.jsonl", name)
	}
	if got := hex.EncodeToString(sha256Sum(raw)); got != meta.SHA256 {
		return nil, fmt.Errorf("table %s checksum mismatch (got %s, manifest %s)", name, got, meta.SHA256)
	}
	var rows [][]byte
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		rows = append(rows, line)
	}
	if len(rows) != meta.Rows {
		return nil, fmt.Errorf("table %s has %d rows, manifest declares %d", name, len(rows), meta.Rows)
	}
	return rows, nil
}

// openArchive reads and validates the zip fully into memory. It parses loudly: a traversal member, an
// over-cap member, a missing or malformed manifest is a hard error and nothing downstream runs.
func openArchive(r io.ReaderAt, size int64) (*archive, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("open archive zip: %w", err)
	}
	a := &archive{data: map[string][]byte{}, uploads: map[string][]byte{}}
	var manifestRaw []byte
	var total int64

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if unsafePath(f.Name) {
			return nil, fmt.Errorf("archive member %q is an unsafe path", f.Name)
		}
		body, rerr := readCapped(f, &total)
		if rerr != nil {
			return nil, fmt.Errorf("read %q: %w", f.Name, rerr)
		}
		name := path.Clean(f.Name)
		switch {
		case name == "manifest.json":
			manifestRaw = body
		case name == "SHA256SUMS":
			// The per-table checksums in the manifest are authoritative; this file is advisory.
		case strings.HasPrefix(name, "data/") && strings.HasSuffix(name, ".jsonl"):
			table := strings.TrimSuffix(strings.TrimPrefix(name, "data/"), ".jsonl")
			a.data[table] = body
		case strings.HasPrefix(name, "uploads/"):
			a.uploads[strings.TrimPrefix(name, "uploads/")] = body
		}
	}

	if manifestRaw == nil {
		return nil, fmt.Errorf("archive has no manifest.json: not one of our export archives")
	}
	if err := json.Unmarshal(manifestRaw, &a.manifest); err != nil {
		return nil, fmt.Errorf("manifest.json is malformed: %w", err)
	}
	return a, nil
}

func unsafePath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return true
	}
	if strings.Contains(name, "\\") || strings.Contains(name, "//") {
		return true
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return true
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func readCapped(f *zip.File, total *int64) ([]byte, error) {
	if f.UncompressedSize64 > maxMemberBytes {
		return nil, fmt.Errorf("member is %d bytes, over the per-member cap", f.UncompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open member: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxMemberBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read member: %w", err)
	}
	if int64(len(data)) > maxMemberBytes {
		return nil, fmt.Errorf("member exceeds the per-member cap")
	}
	*total += int64(len(data))
	if *total > maxTotalBytes {
		return nil, fmt.Errorf("archive uncompressed size exceeds the cap")
	}
	return data, nil
}
