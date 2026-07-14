# flagfish — rules for all agents

**flagfish** (Czech: *flag*) is a CTF platform in Go.

## The mandate

The domain — challenges, flags, solves, scoring, teams — is well understood; the hard part is the
*model*. Every invariant belongs in the database, every fact worth keeping is stamped at the moment
it happens, and every failure is loud. This is meant to be top-tier Go.

The design lives in `docs/` — read it, it is normative:
- `docs/design/DECISIONS.md` — the settled decisions: stack, semantics, scope
- `docs/design/TARGET-FEATURES.md` — unique flags, audit trail, first blood
- `docs/design/ARCHITECTURE.md` + `docs/design/_arch/*` — schema, policy, API, import, hot path,
  layout, testing
- `docs/design/ROADMAP.md` — what comes next
- `docs/adr/*` — the individual architecture decision records

## The stack (settled — do not relitigate)

Postgres 17 only · sqlc + pgx/v5 · goose · River · Huma + chi · React + Vite + TanStack ·
one static binary (`go:embed`) · Apache-2.0

## Non-negotiables

1. **`internal/domain` imports NOTHING.** Not the DB, not HTTP, not River. Pure types and pure
   functions. This is what makes the invariant tests fast and total. CI enforces it.
2. **Back invariants with constraints, not checks.** Every `SELECT`-then-`INSERT` is a race. If the
   database can express the rule, it must. Almost every bug a CTF platform has under load is this
   one bug.
3. **Stamp the fact, don't recompute it.** `solves.value`, `submissions.attributed_account_id`.
   A snapshot is a fact; a join to a mutable row is an opinion.
4. **Loud beats silent.** A parse error beats a silently-wrong config. A hard failure beats a
   quietly-degraded anti-cheat property. Never swallow an error.
5. **The hot path is sacred.** `internal/gameplay` submit: read `docs/design/_arch/05-hotpath.md`
   before touching it. The lock is taken **lazily**, on the correct-flag path only. Wrong answers —
   which are ~99% of submissions — must never take the challenge lock.
6. **No plugin system.** Challenge types and flag types are in-tree interfaces.
7. **No ORM.** sqlc generates from hand-written SQL in `internal/db/queries/`. Never edit
   `internal/db/*.gen.go`.

## Testing

Real Postgres, never a mock — **the invariants live in the constraints, and a mock cannot fail the
way a database can.** `go test -race`. The concurrency suite (`test/concurrency`) is the product
pitch: it drives N goroutines at the same row and proves each invariant holds.

## Comments

Write them like a senior Go engineer, not like a design doc.

- **Sparse.** Comment where a line is load-bearing and surprising; say *why*, never *what*.
  Most code needs none. If a comment restates the code, delete it.
- **Never cite a design doc from code.** No `D8`, `F11`, `C3`, `REQ-…`, no file paths into
  `docs/design/`. Those identifiers are meaningless to someone reading the file and they rot
  the moment the docs move. Explain the reason in plain terms, or say nothing. The long-form
  reasoning lives in `docs/` — leave it there.
- One or two lines, not fifteen. Example: `// FOR NO KEY UPDATE: the FK inserts below take
  FOR KEY SHARE on this row, which FOR UPDATE would block.`

## Conventions

- Errors wrap with `%w` and carry context. No naked `err` returns from a boundary.
- `context.Context` first arg, always. No `context.Background()` outside `main`.
- No `interface{}`/`any` in domain code.
- Table-driven tests. Golden files for the importer.
- Commit messages explain *why*, not *what*.
