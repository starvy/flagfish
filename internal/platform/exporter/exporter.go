// Package exporter serialises a whole instance into our own archive format and restores one back.
//
// It is deliberately NOT the shape we import from: we read foreign CTF archives (internal/platform/
// importer) but we never emit one. Our archive is a versioned, field-masked document of our own —
// a manifest, one newline-delimited JSON file per table, and the content-addressed blob tree.
//
// Two profiles answer two different needs. The default, "safe", is shareable: it strips every auth
// secret (password hashes, the flag material, token hashes) so the artifact can be handed to a third
// party, and it is deliberately NOT restorable to a live login state. "backup" is full fidelity for
// migration and disaster recovery — it carries the hashes and everything needed to stand the
// instance back up — and it is still our format, never a foreign-compatible one.
//
// The read side is generic on purpose: each row is `to_jsonb(row)`, so the column set is
// self-describing and a new column round-trips without a code change, while the table names come from
// a compile-time registry (never the archive) so nothing user-supplied is ever interpolated into SQL.
// Loud beats silent throughout: an inconsistent instance, an unreadable blob, a wrong-profile
// restore, or a format-version mismatch is a hard error, never a silent partial.
package exporter

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/storage"
)

// formatMagic identifies our archive. It is not any foreign importer's shape, and the restorer
// refuses anything whose manifest does not carry it exactly.
const formatMagic = "flagfish/export"

// formatVersion is OUR number, bumped only on a breaking change to the container. It is unrelated to
// the schema version, which pins the goose migration the data was dumped at.
const formatVersion = 1

// Profile selects how much of the instance the archive carries.
type Profile string

const (
	// ProfileSafe is the default: field-masked and shareable, never restorable to a live login state.
	ProfileSafe Profile = "safe"
	// ProfileBackup is full fidelity for migration and disaster recovery, and is restorable.
	ProfileBackup Profile = "backup"
)

// ParseProfile maps a CLI string to a Profile, refusing anything else loudly.
func ParseProfile(s string) (Profile, error) {
	switch Profile(s) {
	case ProfileSafe:
		return ProfileSafe, nil
	case ProfileBackup:
		return ProfileBackup, nil
	default:
		return "", fmt.Errorf("unknown export profile %q (want %q or %q)", s, ProfileSafe, ProfileBackup)
	}
}

// Manifest is the archive's self-description. It declares the format, the schema pin, the profile,
// and — so the masking is auditable from the file alone — exactly what was withheld.
type Manifest struct {
	Format        string               `json:"format"`
	FormatVersion int                  `json:"format_version"`
	SchemaVersion int64                `json:"schema_version"`
	Profile       Profile              `json:"profile"`
	ExportedAt    time.Time            `json:"exported_at"`
	UserMode      string               `json:"user_mode"`
	ProductVer    string               `json:"product_version"`
	Tables        map[string]TableMeta `json:"tables"`
	Files         int                  `json:"files"`
	Omitted       []string             `json:"omitted"`
}

// TableMeta is the per-table checksum and row count. The count is also a guard: the restorer fails
// if the lines it reads do not equal it.
type TableMeta struct {
	Rows   int    `json:"rows"`
	SHA256 string `json:"sha256"`
}

// Report is the human-facing summary an export prints. It mirrors the import report's role: the
// export says out loud what it wrote and what it held back.
type Report struct {
	Profile     Profile
	UserMode    string
	Rows        map[string]int
	Files       int
	UploadBytes int64
	Omitted     []string
}

// table is one registry entry: a table we know how to dump, in FK-dependency order, with its mask.
type table struct {
	name    string
	orderBy string
	// hasSerialID marks a bigserial id whose sequence must be advanced past MAX(id) on restore.
	hasSerialID bool
	// omitInSafe drops the whole table from a safe export (an IP log, an audit trail — no value to a
	// third party and needlessly sensitive).
	omitInSafe bool
	// dropInSafe removes these columns from a safe export. They are auth secrets or flag material.
	dropInSafe []string
	// setInSafe forces these columns to a fixed JSON value in a safe export.
	setInSafe map[string]string
	// maskConfigValues nulls the value of every config row whose key the config package does not
	// declare public, in a safe export.
	maskConfigValues bool
}

// registry is the ordered set of tables an export carries. Order is FK-topological so the restore
// reads legibly; it is a compile-time constant, never derived from the archive. Default-deny: a
// table absent here is never exported, so a new table is "missing", never "leaked" — and a test
// asserts every table at schema head is either here or in the deliberately-excluded set.
var registry = []table{
	{name: "instance", orderBy: "id"},
	{name: "config", orderBy: "id", hasSerialID: true, maskConfigValues: true},
	{name: "brackets", orderBy: "id", hasSerialID: true},
	{name: "teams", orderBy: "id", hasSerialID: true, dropInSafe: []string{"password_hash", "secret"}},
	{
		name: "users", orderBy: "id", hasSerialID: true,
		dropInSafe: []string{"password_hash", "secret"},
		setInSafe:  map[string]string{"must_change_password": "true"},
	},
	{name: "fields", orderBy: "id", hasSerialID: true},
	{name: "field_entries", orderBy: "id", hasSerialID: true},
	{name: "pages", orderBy: "id", hasSerialID: true},
	{name: "challenges", orderBy: "id", hasSerialID: true},
	{name: "files", orderBy: "id", hasSerialID: true},
	{name: "tags", orderBy: "id", hasSerialID: true},
	{name: "flags", orderBy: "id", hasSerialID: true, dropInSafe: []string{"content"}},
	// A hint is bought with points. Handing a mirror or a sponsor an archive mid-event must not hand
	// them the answers the players are paying for.
	{name: "hints", orderBy: "id", hasSerialID: true, dropInSafe: []string{"content"}},
	{name: "challenge_instances", orderBy: "id", hasSerialID: true, dropInSafe: []string{"value_hash"}},
	{name: "flag_issues", orderBy: "challenge_id, account_id"},
	{name: "submissions", orderBy: "id", hasSerialID: true, dropInSafe: []string{"provided", "ip"}},
	{name: "solves", orderBy: "id", hasSerialID: true},
	{name: "awards", orderBy: "id", hasSerialID: true},
	{name: "hint_unlocks", orderBy: "id", hasSerialID: true},
	{name: "tracking", orderBy: "id", hasSerialID: true, omitInSafe: true},
	{name: "notifications", orderBy: "id", hasSerialID: true},
	{name: "audit_log", orderBy: "id", hasSerialID: true, omitInSafe: true},
}

// excludedTables are ours but never in an archive of either profile: live credentials, ephemeral
// counters, and operational bookkeeping. Named so the completeness test can prove nothing was
// forgotten. River's own tables are not ours and are not listed.
var excludedTables = []string{
	"api_tokens", "sessions", "email_tokens", "rate_limits", "tasks",
}

// querier is the read surface Export needs — satisfied by *pgxpool.Pool.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Progress reports coarse advancement of an export or restore, 0..100, with a short human detail.
// The reporter MUST write on a connection separate from the operation itself — a restore is one
// transaction, so a progress write inside it is invisible until commit. A nil Progress disables
// reporting. It is deliberately fallible-free: a progress-write hiccup must never fail a good
// restore, so the caller logs and swallows on its own side.
type Progress func(ctx context.Context, detail string, percent int)

func (p Progress) report(ctx context.Context, detail string, percent int) {
	if p != nil {
		p(ctx, detail, percent)
	}
}

// Export writes the instance behind pool into w as an archive of the given profile. See
// ExportWithProgress; this is the no-progress form the CLI uses.
func Export(ctx context.Context, pool *pgxpool.Pool, store storage.Store, profile Profile, productVer string, w io.Writer) (*Report, error) {
	return ExportWithProgress(ctx, pool, store, profile, productVer, w, nil)
}

// ExportWithProgress writes the instance behind pool into w as an archive of the given profile. File
// blobs stream out of store; an object the store cannot return is a hard error, because an archive
// missing a challenge's attachment is worse than no archive at all. progress, when non-nil, is
// called as each table and the blob tree are written.
func ExportWithProgress(ctx context.Context, pool *pgxpool.Pool, store storage.Store, profile Profile, productVer string, w io.Writer, progress Progress) (*Report, error) {
	if profile != ProfileSafe && profile != ProfileBackup {
		return nil, fmt.Errorf("export: invalid profile %q", profile)
	}

	userMode, err := scanString(ctx, pool, `SELECT user_mode FROM instance WHERE id = true`)
	if err != nil {
		return nil, fmt.Errorf("export: read instance: %w", err)
	}
	schemaVersion, err := scanInt64(ctx, pool, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`)
	if err != nil {
		return nil, fmt.Errorf("export: read schema version: %w", err)
	}

	rep := &Report{Profile: profile, UserMode: userMode, Rows: map[string]int{}}
	manifest := Manifest{
		Format:        formatMagic,
		FormatVersion: formatVersion,
		SchemaVersion: schemaVersion,
		Profile:       profile,
		ExportedAt:    time.Now().UTC(),
		UserMode:      userMode,
		ProductVer:    productVer,
		Tables:        map[string]TableMeta{},
	}

	zw := zip.NewWriter(w)
	var sums []string
	var filesRows [][]byte

	for i, t := range registry {
		progress.report(ctx, "exporting "+t.name, 5+i*85/len(registry))
		if profile == ProfileSafe && t.omitInSafe {
			manifest.Omitted = append(manifest.Omitted, t.name)
			continue
		}
		rows, rerr := readTable(ctx, pool, t)
		if rerr != nil {
			return nil, fmt.Errorf("export: read %s: %w", t.name, rerr)
		}
		if t.name == "files" {
			filesRows = rows
		}

		h := sha256.New()
		entry, cerr := zw.Create("data/" + t.name + ".jsonl")
		if cerr != nil {
			return nil, fmt.Errorf("export: create %s entry: %w", t.name, cerr)
		}
		mw := io.MultiWriter(entry, h)
		for _, raw := range rows {
			masked, merr := applyMask(t, profile, raw)
			if merr != nil {
				return nil, fmt.Errorf("export: mask %s: %w", t.name, merr)
			}
			if _, werr := mw.Write(append(masked, '\n')); werr != nil {
				return nil, fmt.Errorf("export: write %s: %w", t.name, werr)
			}
		}
		sum := hex.EncodeToString(h.Sum(nil))
		manifest.Tables[t.name] = TableMeta{Rows: len(rows), SHA256: sum}
		sums = append(sums, sum+"  data/"+t.name+".jsonl")
		rep.Rows[t.name] = len(rows)
	}

	if profile == ProfileSafe {
		manifest.Omitted = append(manifest.Omitted,
			"api_tokens", "sessions", "email_tokens",
			"users.password_hash", "users.secret", "teams.password_hash", "teams.secret",
			"flags.content", "challenge_instances.value_hash", "hints.content",
			"submissions.provided", "submissions.ip", "config.secret_keys")
	}
	sort.Strings(manifest.Omitted)
	manifest.Omitted = dedupe(manifest.Omitted)

	progress.report(ctx, "exporting file blobs", 90)
	uploadBytes, uploadSums, err := writeUploads(ctx, zw, store, filesRows, rep)
	if err != nil {
		return nil, err
	}
	sums = append(sums, uploadSums...)
	rep.UploadBytes = uploadBytes
	manifest.Files = rep.Files

	if err := writeManifest(zw, &manifest); err != nil {
		return nil, err
	}
	if err := writeEntry(zw, "SHA256SUMS", []byte(strings.Join(sums, "\n")+"\n")); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("export: finalise archive: %w", err)
	}
	rep.Omitted = manifest.Omitted
	return rep, nil
}

// readTable reads one table as a slice of `to_jsonb(row)` documents in a stable order. The values
// round-trip losslessly because Postgres owns both the serialisation here and the reconstruction on
// restore — bytea as hex, timestamps as ISO-8601, jsonb nested verbatim.
func readTable(ctx context.Context, q querier, t table) ([][]byte, error) {
	rows, err := q.Query(ctx, "SELECT to_jsonb(x) FROM "+t.name+" x ORDER BY "+t.orderBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		// Copy: pgx may reuse the scan buffer across iterations.
		out = append(out, append([]byte(nil), raw...))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return out, nil
}

// applyMask returns the row as it belongs in the archive. A backup carries the row verbatim; a safe
// export drops the secret columns, forces the reset flags, and nulls every config value the config
// package does not declare public. Because a safe archive is never restorable, the masked row need
// not be a legal insert — only free of secrets.
func applyMask(t table, profile Profile, raw []byte) ([]byte, error) {
	if profile == ProfileBackup {
		return raw, nil
	}
	if len(t.dropInSafe) == 0 && len(t.setInSafe) == 0 && !t.maskConfigValues {
		return raw, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("decode row: %w", err)
	}
	for _, c := range t.dropInSafe {
		delete(obj, c)
	}
	for k, v := range t.setInSafe {
		obj[k] = json.RawMessage(v)
	}
	if t.maskConfigValues {
		// Default-deny: a key we cannot even read is certainly not one we can vouch for. The
		// importer preserves foreign and plugin keys verbatim, so the key space here is open and
		// only config gets to say which of them are safe to disclose.
		var key string
		if err := json.Unmarshal(obj["key"], &key); err != nil {
			return nil, fmt.Errorf("config row has no readable key (refusing to guess whether its value is a secret): %w", err)
		}
		if config.Secret(key) {
			obj["value"] = json.RawMessage("null")
		}
	}
	masked, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshal masked row: %w", err)
	}
	return masked, nil
}

// writeUploads streams each distinct file blob out of the store into uploads/<sha256>, deduplicating
// by content address. The files table is content-addressed, so many rows may share one object; the
// object is written once.
func writeUploads(ctx context.Context, zw *zip.Writer, store storage.Store, filesRows [][]byte, rep *Report) (total int64, sums []string, err error) {
	seen := map[string]bool{}
	for _, raw := range filesRows {
		var f struct {
			ID        int64  `json:"id"`
			Location  string `json:"location"`
			SHA256sum string `json:"sha256sum"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return 0, nil, fmt.Errorf("export: decode file row: %w", err)
		}
		sha, err := byteaHexToHex(f.SHA256sum)
		if err != nil {
			return 0, nil, fmt.Errorf("export: file %d bad sha256sum: %w", f.ID, err)
		}
		if seen[sha] {
			continue
		}
		seen[sha] = true

		rc, gerr := store.Get(ctx, sha)
		if gerr != nil {
			return 0, nil, fmt.Errorf("export: read object %s for file %d (%s): %w", sha, f.ID, f.Location, gerr)
		}
		entry, cerr := zw.Create("uploads/" + sha)
		if cerr != nil {
			rc.Close()
			return 0, nil, fmt.Errorf("export: create upload entry %s: %w", sha, cerr)
		}
		h := sha256.New()
		n, werr := io.Copy(io.MultiWriter(entry, h), rc)
		rc.Close()
		if werr != nil {
			return 0, nil, fmt.Errorf("export: write upload %s: %w", sha, werr)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != sha {
			return 0, nil, fmt.Errorf("export: object %s hashes to %s: store is corrupt", sha, got)
		}
		total += n
		rep.Files++
		sums = append(sums, sha+"  uploads/"+sha)
	}
	return total, sums, nil
}

func writeManifest(zw *zip.Writer, m *Manifest) error {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("export: marshal manifest: %w", err)
	}
	return writeEntry(zw, "manifest.json", body)
}

func writeEntry(zw *zip.Writer, name string, body []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("export: create %s: %w", name, err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("export: write %s: %w", name, err)
	}
	return nil
}

// byteaHexToHex converts Postgres's bytea JSON encoding ("\\x<hex>") to the bare hex the store keys
// on. Any other shape is a corruption we refuse rather than paper over.
func byteaHexToHex(s string) (string, error) {
	if !strings.HasPrefix(s, `\x`) {
		return "", fmt.Errorf("not a bytea hex string: %q", s)
	}
	return s[2:], nil
}

func scanString(ctx context.Context, q querier, sql string) (string, error) {
	var s string
	if err := q.QueryRow(ctx, sql).Scan(&s); err != nil {
		return "", fmt.Errorf("query %q: %w", sql, err)
	}
	return s, nil
}

func scanInt64(ctx context.Context, q querier, sql string) (int64, error) {
	var n int64
	if err := q.QueryRow(ctx, sql).Scan(&n); err != nil {
		return 0, fmt.Errorf("query %q: %w", sql, err)
	}
	return n, nil
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	var last string
	for i, s := range sorted {
		if i == 0 || s != last {
			out = append(out, s)
		}
		last = s
	}
	return out
}
