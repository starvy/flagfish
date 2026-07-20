//go:build integration

package schema

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/config"
)

// TestConfigKeyInventory pins every registered config key to an operator write path
// or an explicit exemption. One subtest per key; a red subtest names a knob the
// registry declares but no admin route or CLI command can actually set.
//
// The rule is mechanical: a key counts as reachable when its quoted literal appears
// in a file that itself performs a config write — a non-generated, non-test Go file
// under internal/httpapi or cmd that calls Config.Set, or a hand-written query under
// internal/db/queries that inserts into or updates the config table. Requiring the
// write in the same file keeps a read-side mention elsewhere from counting; struct
// tags and comments are stripped before matching, so an echo field's json tag or a
// commented-out key in the same file cannot stand in for a write.
//
// No database is touched — the inputs are the registry and the source tree — but the
// sweep still honours the package's SCHEMA_DATABASE_URL gate so the required sweeps,
// which set only TEST_DATABASE_URL, skip it while the gaps are being closed.
func TestConfigKeyInventory(t *testing.T) {
	if os.Getenv("SCHEMA_DATABASE_URL") == "" {
		t.Skip("SCHEMA_DATABASE_URL is not set — run `task test-schema`")
	}

	writers := cfginvWriterFiles(t)
	for _, key := range config.Keys() {
		t.Run(key, func(t *testing.T) {
			if reason, ok := cfginvExempt[key]; ok {
				if config.Modelled(key) {
					t.Fatalf("exempted as %s, but the key now has a setter — reclassify it, don't carry the exemption", reason)
				}
				t.Logf("exempt: %s", reason)
				return
			}
			var hits []string
			for _, w := range writers {
				if strings.Contains(w.content, w.quote+key+w.quote) {
					hits = append(hits, w.path)
				}
			}
			if len(hits) == 0 {
				t.Errorf("no operator write path: %q appears in no config-writing file", key)
				return
			}
			t.Logf("written in %s", strings.Join(hits, ", "))
		})
	}
}

// cfginvExempt lists keys deliberately not operator-writable, each with the reason.
// Every entry must stay setterless (config.Modelled false); the subtest enforces
// that so a key that grows a setter cannot hide behind a stale exemption.
var cfginvExempt = map[string]string{
	// The account model lives in the instance singleton, fixed at setup; the registry
	// entry is a nil setter that exists only to mark the legacy key as public.
	"user_mode": "disclosure-only: the account model is the instance singleton's, never sourced from config",
}

// cfginvSQLWrite marks a hand-written query file as a config writer.
var cfginvSQLWrite = regexp.MustCompile(`(?i)(insert\s+into|update)\s+config\b`)

// cfginvWriterFile is one source file that performs a config write, held whole for
// literal matching. quote is the string-literal delimiter of the file's language.
type cfginvWriterFile struct {
	path    string
	content string
	quote   string
}

// cfginvWriterFiles collects every config-writing file: Go sources under
// internal/httpapi and cmd that call Config.Set, and queries under
// internal/db/queries that write the config table.
func cfginvWriterFiles(t *testing.T) []cfginvWriterFile {
	t.Helper()

	var writers []cfginvWriterFile
	for _, root := range []string{repoPath(t, "internal", "httpapi"), repoPath(t, "cmd")} {
		for _, p := range cfginvSources(t, root, ".go") {
			content := cfginvRead(t, p)
			if strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, ".gen.go") ||
				strings.HasPrefix(content, "// Code generated") {
				continue
			}
			if stripped := cfginvStripGo(content); strings.Contains(stripped, "Config.Set(") {
				writers = append(writers, cfginvWriterFile{path: p, content: stripped, quote: `"`})
			}
		}
	}
	for _, p := range cfginvSources(t, repoPath(t, "internal", "db", "queries"), ".sql") {
		content := cfginvStripSQLComments(cfginvRead(t, p))
		if cfginvSQLWrite.MatchString(content) {
			writers = append(writers, cfginvWriterFile{path: p, content: content, quote: `'`})
		}
	}

	if len(writers) == 0 {
		t.Fatal("found no config-writing files at all — the writer-detection rule has rotted, fix it before trusting any subtest")
	}
	return writers
}

// cfginvSources lists every file under root with the given extension, sorted by walk
// order so subtest logs are stable.
func cfginvSources(t *testing.T, root, ext string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(p) == ext {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return paths
}

func cfginvRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// cfginvStripGo removes raw string literals and line comments, so a struct tag
// (`json:"paused"`) or a commented-out key never counts as a write target.
var (
	cfginvGoRawString = regexp.MustCompile("`[^`]*`")
	cfginvGoComment   = regexp.MustCompile(`//[^\n]*`)
)

func cfginvStripGo(content string) string {
	return cfginvGoComment.ReplaceAllString(cfginvGoRawString.ReplaceAllString(content, ""), "")
}

// cfginvStripSQLComments removes `--` comments so prose mentioning a key in quotes
// never counts as a write target.
var cfginvSQLComment = regexp.MustCompile(`--[^\n]*`)

func cfginvStripSQLComments(content string) string {
	return cfginvSQLComment.ReplaceAllString(content, "")
}
