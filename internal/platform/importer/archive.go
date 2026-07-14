package importer

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
)

// Caps reject a hostile archive before a byte of it reaches the database: a zip bomb, a member that
// decompresses to gigabytes, or a dump with millions of members. Zero means "use the default".
type Caps struct {
	MaxMemberBytes int64 // per uncompressed member
	MaxTotalBytes  int64 // sum of all uncompressed members
	MaxMembers     int   // number of zip entries
}

func (c Caps) withDefaults() Caps {
	if c.MaxMemberBytes == 0 {
		c.MaxMemberBytes = 512 << 20 // 512 MiB
	}
	if c.MaxTotalBytes == 0 {
		c.MaxTotalBytes = 4 << 30 // 4 GiB
	}
	if c.MaxMembers == 0 {
		c.MaxMembers = 500_000
	}
	return c
}

// Archive is a parsed, validated import source: table dumps and the upload tree, held in memory.
type Archive struct {
	Revision string
	Ordinal  int
	CTFdHint string

	tables  map[string]*tableEnvelope
	uploads map[string][]byte // keyed by the path under uploads/, e.g. "d41d8c/flag.txt"
}

// unsafePath reports whether a zip member name could escape the extraction root.
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

// openArchive reads and validates a zip, returning a fully in-memory Archive. It parses loudly: a
// malformed table dump, a path-traversal member, or an over-cap member is a hard error and nothing
// downstream runs.
func openArchive(r io.ReaderAt, size int64, caps Caps, assumeRevision string) (*Archive, error) {
	caps = caps.withDefaults()
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("open archive zip: %w", err)
	}
	if len(zr.File) > caps.MaxMembers {
		return nil, fmt.Errorf("archive has %d members, over the cap of %d", len(zr.File), caps.MaxMembers)
	}

	a := &Archive{tables: map[string]*tableEnvelope{}, uploads: map[string][]byte{}}
	var total int64
	sawDB := false

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if unsafePath(f.Name) {
			return nil, fmt.Errorf("archive member %q is an unsafe path (traversal or absolute)", f.Name)
		}
		if caps.MaxMemberBytes >= 0 && f.UncompressedSize64 > uint64(caps.MaxMemberBytes) {
			return nil, fmt.Errorf("archive member %q is %d bytes, over the per-member cap of %d",
				f.Name, f.UncompressedSize64, caps.MaxMemberBytes)
		}

		name := path.Clean(f.Name)
		switch {
		case name == "db/alembic_version.json" || strings.HasPrefix(name, "db/") && strings.HasSuffix(name, ".json"):
			sawDB = true
			data, rerr := readCapped(f, caps.MaxMemberBytes, &total, caps.MaxTotalBytes)
			if rerr != nil {
				return nil, fmt.Errorf("read %q: %w", f.Name, rerr)
			}
			table := strings.TrimSuffix(strings.TrimPrefix(name, "db/"), ".json")
			var env tableEnvelope
			if uerr := json.Unmarshal(data, &env); uerr != nil {
				return nil, fmt.Errorf("archive table %q is not a valid table dump: %w", table, uerr)
			}
			a.tables[table] = &env

		case strings.HasPrefix(name, "uploads/"):
			data, rerr := readCapped(f, caps.MaxMemberBytes, &total, caps.MaxTotalBytes)
			if rerr != nil {
				return nil, fmt.Errorf("read %q: %w", f.Name, rerr)
			}
			a.uploads[strings.TrimPrefix(name, "uploads/")] = data
		}
	}

	if !sawDB {
		return nil, fmt.Errorf("archive has no db/ directory: not a recognised export archive")
	}

	alembic := a.tables["alembic_version"]
	if alembic == nil || len(alembic.Results) == 0 {
		return nil, fmt.Errorf("archive is missing db/alembic_version.json: cannot determine schema revision")
	}
	var rev alembicRow
	if uerr := json.Unmarshal(alembic.Results[0], &rev); uerr != nil {
		return nil, fmt.Errorf("archive alembic_version is malformed: %w", uerr)
	}
	ord, aerr := admit(rev.VersionNum, assumeRevision)
	if aerr != nil {
		return nil, aerr
	}
	a.Revision = rev.VersionNum
	a.Ordinal = ord
	a.CTFdHint = a.configValue("ctf_version")
	return a, nil
}

// readCapped reads a member fully while charging its bytes against the running total.
func readCapped(f *zip.File, memberCap int64, total *int64, totalCap int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open member: %w", err)
	}
	defer rc.Close()

	// LimitReader guards against a lying UncompressedSize64 in a crafted zip.
	data, err := io.ReadAll(io.LimitReader(rc, memberCap+1))
	if err != nil {
		return nil, fmt.Errorf("read member: %w", err)
	}
	if int64(len(data)) > memberCap {
		return nil, fmt.Errorf("member exceeds the per-member cap of %d bytes", memberCap)
	}
	*total += int64(len(data))
	if *total > totalCap {
		return nil, fmt.Errorf("archive uncompressed size exceeds the cap of %d bytes", totalCap)
	}
	return data, nil
}

// rows unmarshals every row of a table into T. A table absent from the archive yields nil, which the
// caller treats as "the source had none".
func decodeTable[T any](a *Archive, table string) ([]T, error) {
	env := a.tables[table]
	if env == nil {
		return nil, nil
	}
	out := make([]T, 0, len(env.Results))
	for i, raw := range env.Results {
		var row T
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, fmt.Errorf("archive table %q row %d: %w", table, i, err)
		}
		out = append(out, row)
	}
	return out, nil
}

// configValue returns a single config value from the archive, last-writer-wins by id, without
// running the full translation. Used for advisory fields like ctf_version.
func (a *Archive) configValue(key string) string {
	env := a.tables["config"]
	if env == nil {
		return ""
	}
	best := int64(-1)
	val := ""
	for _, raw := range env.Results {
		var c ctfdConfig
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		if c.Key == key && c.ID >= best && c.Value != nil {
			best = c.ID
			val = *c.Value
		}
	}
	return val
}
