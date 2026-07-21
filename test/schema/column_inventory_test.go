//go:build integration

package schema

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestColumnInventory sweeps every column of the admin-managed tables and requires both
// halves of its lifecycle, or an explicit exemption:
//
//   - Writer: the column is an INSERT column-list or UPDATE SET target in
//     internal/db/queries/*.sql outside import.sql. A column only the importer writes
//     cannot be set from the running app at all.
//   - Reader: the sqlc field for the column (mapped from the model structs in
//     internal/db/models.go, so the camel-casing is never guessed) appears as a `.Field`
//     selector in hand-written Go under internal/ and cmd/. A SELECT list proves nothing —
//     `RETURNING *` fetches every column — so the scan excludes the surfaces that touch
//     columns wholesale without acting on them: internal/db (generated), the
//     importer/exporter (an archive round-trip copies every column by construction), and
//     the admin CRUD echo (internal/adminops, httpapi/handlers_admin*), which only hands
//     the stored value back to the form that wrote it. A column with no Go consumer still
//     passes, classified "sql-read", when a non-import query uses it semantically
//     (WHERE / JOIN ON / GROUP BY / ORDER BY / HAVING).
//
// Matching is by name, not by type: a same-named field on another struct can mask a dead
// column — a false pass, never a false failure. ON CONFLICT ... DO UPDATE SET targets are
// not parsed; no query on these tables uses one today.
//
// A red subtest is a named backlog item — an unbuilt admin write path, or a stored value
// nothing consumes. Do not tune the detector to green it; build the missing half or add an
// exemption with a reason.
func TestColumnInventory(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	columns := colinvSchemaColumns(ctx, t, conn)

	sqlFiles := colinvLoadSQL(t, repoPath(t, "internal", "db", "queries"))
	writers := colinvScanWriters(sqlFiles)
	semantic := colinvScanSemantic(sqlFiles, columns)
	fields := colinvModelFields(t, repoPath(t, "internal", "db", "models.go"))
	selectors := colinvGoSelectors(t, repoPath(t, "internal"), repoPath(t, "cmd"))

	for key := range colinvExempt {
		table, col, _ := strings.Cut(key, ".")
		if !colinvContains(columns[table], col) {
			t.Errorf("exemption %q names a column that is not at schema head — delete it", key)
		}
	}

	for _, table := range colinvTables {
		for _, col := range columns[table] {
			t.Run(table+"."+col, func(t *testing.T) {
				key := table + "." + col
				if why, ok := colinvExempt[key]; ok {
					t.Logf("exempt: %s", why)
					return
				}

				field := colinvFieldFor(fields[colinvModelStruct[table]], col)
				if field == "" {
					t.Fatalf("column %s is at schema head but unclassified: no field on db.%s — regenerate sqlc or exempt it",
						key, colinvModelStruct[table])
				}

				w := writers[table][col]
				switch {
				case w != nil && len(w.app) > 0:
					t.Logf("written by %s", strings.Join(w.app, ", "))
				case w != nil && w.imp:
					t.Errorf("no writer outside the importer: the only writer for %s is import.sql — the admin app cannot set it", key)
				default:
					t.Errorf("no write path in any query file, importer included: nothing inserts or updates %s "+
						"(dynamic SQL such as the backup restore is invisible to this sweep)", key)
				}

				if file, ok := selectors[field]; ok {
					t.Logf("read in Go via .%s (e.g. %s)", field, file)
				} else if uses := semantic[table][col]; len(uses) > 0 {
					t.Logf("sql-read only: no Go consumer of .%s; used in WHERE/JOIN/ORDER BY of %s", field, strings.Join(uses, ", "))
				} else {
					t.Errorf("no reader: .%s is never referenced in hand-written Go outside the admin echo surface, "+
						"and no query uses %s in a WHERE/JOIN/ORDER BY — the value is write-only", field, col)
				}
			})
		}
	}
}

var colinvTables = []string{"challenges", "tags", "hints", "teams", "users", "challenge_instances"}

var colinvModelStruct = map[string]string{
	"challenges":          "Challenge",
	"tags":                "Tag",
	"hints":               "Hint",
	"teams":               "Team",
	"users":               "User",
	"challenge_instances": "ChallengeInstance",
}

// Columns that are by design not admin-managed data: table.column → one-line reason.
var colinvExempt = map[string]string{
	"challenges.id":          "surrogate key, assigned by bigserial",
	"tags.id":                "surrogate key, assigned by bigserial",
	"hints.id":               "surrogate key, assigned by bigserial",
	"teams.id":               "surrogate key, assigned by bigserial",
	"users.id":               "surrogate key, assigned by bigserial",
	"challenge_instances.id": "surrogate key, assigned by bigserial",
	"challenges.created_at":  "DB-stamped on insert (DEFAULT now())",
	"challenges.updated_at":  "DB-stamped alongside every write (SET updated_at = now())",
	"teams.created_at":       "DB-stamped on insert (DEFAULT now())",
	"users.created_at":       "DB-stamped on insert (DEFAULT now())",
	"users.secret":           "reserved: CTFd-parity import fidelity — carried by import/export; vestigial even upstream (no reader in CTFd master, invite codes use password); never surfaced by the app",
	"teams.secret":           "reserved: CTFd-parity import fidelity — see users.secret",
}

// colinvSchemaColumns enumerates the live columns of the six tables in ordinal order, and
// fails loudly if a table is missing — the inventory must never silently shrink.
func colinvSchemaColumns(ctx context.Context, t *testing.T, conn *pgx.Conn) map[string][]string {
	t.Helper()
	rows, err := conn.Query(ctx, `
		SELECT table_name, column_name
		  FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = ANY($1)
		 ORDER BY table_name, ordinal_position`, colinvTables)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var table, col string
		if err := rows.Scan(&table, &col); err != nil {
			t.Fatal(err)
		}
		out[table] = append(out[table], col)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range colinvTables {
		if len(out[table]) == 0 {
			t.Fatalf("table %s is not at schema head — the inventory would silently shrink", table)
		}
	}
	return out
}

var colinvIdentRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

type colinvSQLFile struct {
	name     string // base name, e.g. "admin.sql"
	isImport bool
	stmts    []string // comment-stripped statements
}

func colinvLoadSQL(t *testing.T, dir string) []colinvSQLFile {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read queries dir: %v", err)
	}
	commentRE := regexp.MustCompile(`--[^\n]*`)
	var files []colinvSQLFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		text := commentRE.ReplaceAllString(string(raw), "")
		files = append(files, colinvSQLFile{
			name:     e.Name(),
			isImport: e.Name() == "import.sql",
			stmts:    strings.Split(text, ";"),
		})
	}
	if len(files) == 0 {
		t.Fatal("no .sql files found — the sweep would be vacuous")
	}
	return files
}

type colinvWriterSet struct {
	app []string // non-import query files writing the column
	imp bool     // import.sql writes it
}

var (
	colinvInsertRE = regexp.MustCompile(`(?is)\binsert\s+into\s+([a-z_]+)\s*\(([^)]*)\)`)
	colinvUpdateRE = regexp.MustCompile(`(?is)\bupdate\s+([a-z_]+)(?:\s+(?:as\s+)?([a-z_]+))?\s+set\b`)
)

func colinvScanWriters(files []colinvSQLFile) map[string]map[string]*colinvWriterSet {
	out := map[string]map[string]*colinvWriterSet{}
	add := func(table, col, file string, isImport bool) {
		if _, ok := colinvModelStruct[table]; !ok {
			return
		}
		if out[table] == nil {
			out[table] = map[string]*colinvWriterSet{}
		}
		w := out[table][col]
		if w == nil {
			w = &colinvWriterSet{}
			out[table][col] = w
		}
		if isImport {
			w.imp = true
		} else if !colinvContains(w.app, file) {
			w.app = append(w.app, file)
			sort.Strings(w.app)
		}
	}
	for _, f := range files {
		for _, stmt := range f.stmts {
			for _, m := range colinvInsertRE.FindAllStringSubmatch(stmt, -1) {
				table := strings.ToLower(m[1])
				for _, c := range strings.Split(m[2], ",") {
					c = strings.ToLower(strings.TrimSpace(c))
					if colinvIdentRE.MatchString(c) {
						add(table, c, f.name, f.isImport)
					}
				}
			}
			for _, m := range colinvUpdateRE.FindAllStringSubmatchIndex(stmt, -1) {
				table := strings.ToLower(stmt[m[2]:m[3]])
				for _, c := range colinvSetTargets(stmt[m[1]:]) {
					add(table, c, f.name, f.isImport)
				}
			}
		}
	}
	return out
}

// colinvSetTargets extracts the assignment targets of a SET clause: identifiers followed by
// `=` at paren depth 0, one per depth-0 comma, stopping at WHERE/FROM/RETURNING.
func colinvSetTargets(s string) []string {
	var cols []string
	depth := 0
	expect := true
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			i++
		case c == ',' && depth == 0:
			expect = true
			i++
		case colinvIdentStart(c):
			j := i
			for j < len(s) && colinvIdentChar(s[j]) {
				j++
			}
			word := strings.ToLower(s[i:j])
			if depth == 0 && (word == "where" || word == "from" || word == "returning") {
				return cols
			}
			if expect && depth == 0 {
				k := j
				for k < len(s) && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n' || s[k] == '\r') {
					k++
				}
				if k < len(s) && s[k] == '=' {
					cols = append(cols, word)
				}
				expect = false
			}
			i = j
		default:
			i++
		}
	}
	return cols
}

// colinvScanSemantic records, per table column, the non-import query files in which the
// column name appears inside a semantic clause (WHERE/ON/GROUP/ORDER/HAVING/USING) or on
// the right-hand side of a SET assignment — an UPDATE that computes one column from others
// reads those others — of a statement that mentions the table. Name-based: a same-named
// column of a joined table also counts, which can only over-report readers.
func colinvScanSemantic(files []colinvSQLFile, columns map[string][]string) map[string]map[string][]string {
	tableRE := map[string]*regexp.Regexp{}
	for _, table := range colinvTables {
		tableRE[table] = regexp.MustCompile(`(?i)\b` + table + `\b`)
	}
	out := map[string]map[string][]string{}
	for _, f := range files {
		if f.isImport {
			continue
		}
		for _, stmt := range f.stmts {
			idents := colinvSemanticIdents(stmt)
			for _, table := range colinvTables {
				if !tableRE[table].MatchString(stmt) {
					continue
				}
				for _, col := range columns[table] {
					if !idents[col] {
						continue
					}
					if out[table] == nil {
						out[table] = map[string][]string{}
					}
					if !colinvContains(out[table][col], f.name) {
						out[table][col] = append(out[table][col], f.name)
					}
				}
			}
		}
	}
	return out
}

// colinvSemanticIdents walks one statement tracking the current clause, pushing it at `(`
// and popping at `)` so a subquery's WHERE does not bleed into the outer SELECT list, and
// collects every identifier seen inside a semantic clause. Inside SET it collects only
// identifiers right of an `=`, and never the assignment's own column: `x = COALESCE(narg, x)`
// is the keep-current-value idiom of a writer, not a read, while `value = f(initial, decay)`
// genuinely reads the inputs.
func colinvSemanticIdents(stmt string) map[string]bool {
	out := map[string]bool{}
	clause := ""
	var stack []string
	setRHS := false // inside SET, past the `=` of the current assignment
	setDepth := 0   // paren depth at which the SET clause itself sits
	setTarget := "" // the column the current SET assignment writes
	semantic := func(c string) bool {
		return c == "where" || c == "on" || c == "having" || c == "order" || c == "group" || c == "using"
	}
	for i := 0; i < len(stmt); {
		c := stmt[i]
		switch {
		case c == '(':
			stack = append(stack, clause)
			i++
		case c == ')':
			if n := len(stack); n > 0 {
				clause = stack[n-1]
				stack = stack[:n-1]
			}
			i++
		case c == '=' && clause == "set":
			setRHS = true
			i++
		case c == ',' && clause == "set" && len(stack) == setDepth:
			setRHS = false // next assignment's target follows
			setTarget = ""
			i++
		case colinvIdentStart(c):
			j := i
			for j < len(stmt) && colinvIdentChar(stmt[j]) {
				j++
			}
			switch w := strings.ToLower(stmt[i:j]); w {
			case "select", "set", "values", "returning", "from", "join",
				"where", "on", "having", "order", "group", "using":
				clause = w
				setRHS = false
				setTarget = ""
				if w == "set" {
					setDepth = len(stack)
				}
			default:
				switch {
				case semantic(clause):
					out[w] = true
				case clause == "set" && !setRHS && setTarget == "":
					setTarget = w
				case clause == "set" && setRHS && w != setTarget:
					out[w] = true
				}
			}
			i = j
		default:
			i++
		}
	}
	return out
}

// colinvModelFields parses the sqlc model file and returns struct name → field names, so
// the column→field mapping comes from what sqlc actually generated.
func colinvModelFields(t *testing.T, path string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	wanted := map[string]bool{}
	for _, s := range colinvModelStruct {
		wanted[s] = true
	}
	out := map[string][]string{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || !wanted[ts.Name.Name] {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, f := range st.Fields.List {
				for _, n := range f.Names {
					out[ts.Name.Name] = append(out[ts.Name.Name], n.Name)
				}
			}
		}
	}
	for s := range wanted {
		if len(out[s]) == 0 {
			t.Fatalf("model struct db.%s not found in %s", s, path)
		}
	}
	return out
}

// colinvFieldFor matches a column to its generated field by case- and underscore-blind
// comparison ("next_id" ↔ "NextID"), never by re-deriving sqlc's casing rules.
func colinvFieldFor(fields []string, col string) string {
	want := strings.ReplaceAll(col, "_", "")
	for _, f := range fields {
		if strings.EqualFold(f, want) {
			return f
		}
	}
	return ""
}

// colinvGoSelectors returns every `.Field` selector (not followed by `(`, so calls are
// out) used in hand-written, non-test Go, mapped to one example file. Excluded wholesale:
// internal/db, the importer/exporter, and the admin echo surface.
func colinvGoSelectors(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	skipDirSuffix := []string{
		"/internal/db",
		"/internal/platform/importer",
		"/internal/platform/exporter",
		"/internal/adminops",
	}
	skipDirBase := map[string]bool{"node_modules": true, "dist": true, "testdata": true}
	selRE := regexp.MustCompile(`\.([A-Z][A-Za-z0-9_]*)`)
	out := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			slash := filepath.ToSlash(path)
			if d.IsDir() {
				if skipDirBase[d.Name()] {
					return filepath.SkipDir
				}
				for _, suf := range skipDirSuffix {
					if strings.HasSuffix(slash, suf) {
						return filepath.SkipDir
					}
				}
				return nil
			}
			base := d.Name()
			if !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") ||
				strings.HasPrefix(base, "handlers_admin") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(content)
			for _, m := range selRE.FindAllStringSubmatchIndex(text, -1) {
				if m[1] < len(text) && text[m[1]] == '(' {
					continue
				}
				name := text[m[2]:m[3]]
				if _, ok := out[name]; !ok {
					out[name] = strings.TrimPrefix(slash, "../../")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}

func colinvContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func colinvIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func colinvIdentChar(c byte) bool {
	return colinvIdentStart(c) || (c >= '0' && c <= '9')
}
