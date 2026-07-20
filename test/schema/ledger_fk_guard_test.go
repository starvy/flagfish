//go:build integration

package schema

import (
	"context"
	"fmt"
	"testing"
)

// fkgLedgerTables are the append-only score-history tables. A row in one is a stamped fact —
// a scoreboard sum, an audit entry, a first-blood claim — so no parent delete may quietly
// erase it, and no delete rule that silently rewrites a row (CASCADE, SET NULL, SET DEFAULT)
// is acceptable; only RESTRICT and NO ACTION turn a destructive delete into a loud error.
var fkgLedgerTables = []string{"solves", "submissions", "awards", "hint_unlocks"}

// fkgExempt names the FKs allowed a destructive delete rule, each with its recorded reason.
// Anything not listed here fails, so a future migration must either pick RESTRICT or add an
// entry — a decision, not an accident.
var fkgExempt = map[string]string{
	// 00003: deleting an attempt must not retract points, so the solve survives with the
	// provenance edge nulled rather than blocking the delete.
	"solves.submission_id -> submissions": "SET NULL chosen deliberately in 00003",
}

// fkgForeignKey is one introspected FK whose child is a ledger table.
type fkgForeignKey struct {
	name       string // constraint name
	childTable string
	childCols  string
	parent     string
	delRule    string // pg_constraint.confdeltype: c=cascade, r=restrict, a=no action, n=set null, d=set default
}

// TestLedgerFKGuard reads the live schema and fails for every foreign key on a ledger table
// whose delete rule is CASCADE. It introspects rather than naming constraints so a future
// migration that adds a new CASCADE into the ledger is caught the day it lands.
func TestLedgerFKGuard(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	const q = `
		SELECT con.conname,
		       child.relname  AS child_table,
		       parent.relname AS parent_table,
		       con.confdeltype::text,
		       (SELECT string_agg(att.attname, ',' ORDER BY u.ord)
		          FROM unnest(con.conkey) WITH ORDINALITY AS u(attnum, ord)
		          JOIN pg_attribute att
		            ON att.attrelid = con.conrelid AND att.attnum = u.attnum) AS child_cols
		  FROM pg_constraint con
		  JOIN pg_class     child  ON child.oid  = con.conrelid
		  JOIN pg_namespace ns     ON ns.oid     = child.relnamespace
		  JOIN pg_class     parent ON parent.oid = con.confrelid
		 WHERE con.contype = 'f'
		   AND ns.nspname   = 'public'
		   AND child.relname = ANY($1)
		 ORDER BY child.relname, con.conname`

	rows, err := conn.Query(ctx, q, fkgLedgerTables)
	if err != nil {
		t.Fatalf("introspect ledger FKs: %v", err)
	}
	defer rows.Close()

	var fks []fkgForeignKey
	for rows.Next() {
		var fk fkgForeignKey
		if err := rows.Scan(&fk.name, &fk.childTable, &fk.parent, &fk.delRule, &fk.childCols); err != nil {
			t.Fatalf("scan FK row: %v", err)
		}
		fks = append(fks, fk)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate FK rows: %v", err)
	}

	// Vacuous-pass guard: a ledger table with zero introspected FKs means the query broke or
	// the table was renamed, and the whole sweep would pass by finding nothing to check.
	found := make(map[string]int, len(fkgLedgerTables))
	for _, fk := range fks {
		found[fk.childTable]++
	}
	for _, tbl := range fkgLedgerTables {
		if found[tbl] == 0 {
			t.Fatalf("no FKs found for table %s — introspection query broken or table renamed?", tbl)
		}
	}

	rule := map[string]string{"c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}
	// A stale exemption is a lie about the schema: fail if it no longer matches a
	// destructive FK.
	seen := make(map[string]bool, len(fks))
	for _, fk := range fks {
		if _, destructive := rule[fk.delRule]; destructive {
			seen[fmt.Sprintf("%s.%s -> %s", fk.childTable, fk.childCols, fk.parent)] = true
		}
	}
	for name := range fkgExempt {
		if !seen[name] {
			t.Errorf("exemption %q matches no destructive FK at schema head — delete it", name)
		}
	}
	for _, fk := range fks {
		name := fmt.Sprintf("%s.%s -> %s", fk.childTable, fk.childCols, fk.parent)
		t.Run(name, func(t *testing.T) {
			destructive, ok := rule[fk.delRule]
			if !ok {
				return
			}
			if reason, exempt := fkgExempt[name]; exempt {
				t.Logf("exempt: ON DELETE %s — %s", destructive, reason)
				return
			}
			t.Errorf("%s is ON DELETE %s: deleting a %s row would silently rewrite %s history — use RESTRICT or NO ACTION",
				fk.name, destructive, fk.parent, fk.childTable)
		})
	}
}
