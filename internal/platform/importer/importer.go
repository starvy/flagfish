// Package importer restores a foreign CTF export archive into the flagfish schema.
//
// It is one-way by decision: an archive comes in, translated into our tables; nothing goes back out
// in that format. The pipeline is three pure-ish stages — parse (archive.go), translate
// (translate.go), load (load.go) — with the report as the first-class artifact of every run. Loud
// beats silent throughout: an unreadable archive, an unsolvable flag, an unknown scoring rule, or an
// internally inconsistent dump is a hard error, and the restore is a single transaction that leaves
// the instance untouched on any failure.
package importer

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run imports the archive at path into the database behind pool, returning the report. The report is
// returned even on failure when one exists, so a caller can render what was learned before the error.
func Run(ctx context.Context, pool *pgxpool.Pool, path string, opts Options) (*Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("import: open archive: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("import: stat archive: %w", err)
	}

	started := time.Now().UTC()
	a, err := openArchive(f, info.Size(), opts.Caps, opts.AssumeRevision)
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}

	plan, rep, err := Translate(a, opts)
	rep.StartedAt = started
	if err != nil {
		rep.EndedAt = time.Now().UTC()
		return rep, fmt.Errorf("import: translate: %w", err)
	}
	if rep.hasError() {
		rep.EndedAt = time.Now().UTC()
		return rep, fmt.Errorf("import: archive has blocking findings; refusing to commit")
	}

	if err := Load(ctx, pool, plan); err != nil {
		rep.EndedAt = time.Now().UTC()
		return rep, err
	}
	rep.EndedAt = time.Now().UTC()
	return rep, nil
}
