// Package migrations embeds the goose migrations into the binary.
//
// It exists because a //go:embed directive cannot reach outside its own directory,
// so the embed has to live HERE, beside the SQL — nowhere else in the tree can see
// these files at compile time.
//
// Nothing is read from disk at runtime: `flagfish serve` must run from an empty
// directory against a fresh Postgres.
package migrations

import "embed"

// FS holds every .sql migration, in order. The migrations are the schema's source
// of truth, and this is how they ship.
//
//go:embed *.sql
var FS embed.FS
