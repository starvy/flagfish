# ADR-0007: No plugin system. Ever.

- **Status:** Accepted
- **Date:** 2026-07-14

## Context

A runtime plugin system is the conventional way a CTF platform lets people add challenge types
(dynamic scoring, multiple choice), flag types (regex), and whole features without touching core.
It is a genuinely useful thing to have, and organizers with an unusual requirement have come to
expect it.

The pull to reproduce it is strong, and it should be resisted, for a reason that is about Go and not
about product ambition.

## Options considered

- **(a) A plugin system** — a `plugins/` directory, scanned and loaded at runtime.
- **(b) Go `plugin` package / `.so` loading.**
- **(c) An out-of-process extension mechanism** — gRPC, WASM, or similar.
- **(d) In-tree interfaces.**

## Decision

**(d). Challenge types and flag types are in-tree Go interfaces. Nothing is loaded at runtime.**

The reason is blunt: **"plugin system" in Go is either a distributed system or a recompile.**

- Go's `plugin` package requires the plugin and the host to be built with *the identical* Go
  version, the identical package versions, and the same build flags. It does not work on Windows,
  it does not work with CGO disabled, and in practice it is a support nightmare that ships as a
  feature.
- An out-of-process extension mechanism (gRPC, WASM) is a **distributed system**. Now the flag
  comparison — which lives inside a locked database transaction — is a network call, or a sandboxed
  VM invocation, with its own failure modes, timeouts, and versioning story. Inside the hot path.
- And **what does the extension surface actually need to be?** In practice, two things: a challenge
  type and a flag type. Two interfaces. Ship them in-tree.

We are also honest about the second-order effect, because it is the real one: **a plugin system is a
permanent API surface.** The moment plugins exist, every internal type they touch is frozen, and the
schema changes that this project depends on — adding a constraint, normalizing a table, stamping a
value — become breaking changes to somebody's plugin. A plugin system would make
[ADR-0001](0001-clean-schema-with-import-adapter.md) un-maintainable.

**We keep two seams**, and only two, because each has a second implementation that is real:

- **`FlagIssuer`** — the pool-based implementation today; HMAC templating is a drop-in second one.
- **`ArtifactStore`** — local disk today; S3 is the obvious second.

The rule for adding a seam: **build one only if it has two real implementations today, or if
retrofitting it later would require a schema migration.** Exactly two qualify.

## Consequences

### What this buys

- **The binary is complete.** Nothing is scanned, loaded, or resolved at runtime — which is what makes
  the single-static-binary deployment possible at all.
- **What compiles is what ships.** No version-skew class of bug, no "works on my instance."
- The schema stays ours. We can add a constraint without breaking a third party.
- No sandboxing story, no plugin trust model, no plugin marketplace to police.

### ⚠️ What we gave up

**This is a real, material loss, and it is the strongest argument against this project.** We have no
extension ecosystem, and we will never have one in that form — no third-party challenge types, no
integrations, none of the oddities that serve exactly one CTF's needs.

**Every extension is now a fork or a PR.** If you need a challenge type we do not have, your options
are: upstream it, or maintain a fork. For an organizer with an unusual requirement and no Go
engineer, that is a worse experience than dropping a file into a `plugins/` directory. We are
choosing to serve the person who wants a defensible scoreboard over the person who wants to add a
feature without recompiling — and those are sometimes the same person, and they will be annoyed.

**Themes are gone too**, for the same reason (deferred, not merely unimplemented). Organizers
visually customize their instances far more than people expect.

**We must therefore be *generous* about upstreaming.** A refused PR is now a fork, not an
inconvenience. If this project is stingy about merging challenge types, this ADR becomes a tax on
users rather than a design property.

### What would change this decision

Nothing about Go's plugin story is going to change. What could change is the *shape* of the
extension point: if a genuinely large set of challenge types accumulates, a **declarative** challenge
type (data, not code — a scoring formula plus a compare mode, expressed in the challenge's TOML) is
a viable evolution that keeps the binary complete. That is the direction to explore. It is not a
plugin system.
