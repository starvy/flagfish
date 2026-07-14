# _arch/08 — Challenge-as-code and `flagfishctl`

**Why this exists.** Challenge authors are engineers; they want git, not a web form. And once
challenges are files, three things follow that are worth more than the convenience: challenges get
**code review**, challenges get **CI**, and challenges become **machine-generable**.

---

## Format: TOML frontmatter + Markdown body, one directory per challenge

```
challenges/
  pwn/
    heap-overflow/
      challenge.md          # +++ TOML frontmatter +++ then markdown body (= the description)
      instances.jsonl       # flag mode "unique": one bundle per line
      files/heap            # artifacts
      solve/solve.py        # optional — see § Challenge CI
```

```toml
+++
name     = "heap-overflow"
category = "pwn"
tags     = ["heap", "glibc-2.35"]
state    = "visible"                 # visible | hidden

[value]
type    = "dynamic"                  # static | dynamic
initial = 500
decay   = 25
minimum = 100

[flags]
mode      = "unique"                 # static | unique   — how flags are ISSUED
instances = "./instances.jsonl"      # required iff mode = "unique"
# for mode = "static":
#   [[flags.static]]
#   content = "CTF{h3ap_pwn}"
#   type    = "static"               # static | regex    — how a flag is COMPARED

[first_blood]
mode  = "announce"                   # none | announce | bonus
bonus = 100                          # iff mode = "bonus"

[[hints]]
content = "look at what free() does to tcache"
cost    = 50

[[files]]
path = "./files/heap"

[solve]                              # optional. see § Challenge CI
cmd = "python solve/solve.py $HOST $PORT"
+++

# Heap Overflow

Connect: `nc chal.ctf {{ .Port }}`
Download: [heap]({{ .ArtifactURL }})

glibc 2.35. The binary is not stripped.
```

### Why TOML and not YAML

The config is mostly flat with a few arrays; TOML handles that cleanly. The decisive reasons are
about **failure modes**, and they matter more here than usual, because these files will often be
machine-generated:

| | YAML | TOML |
|---|---|---|
| Indentation | significant — nested indent errors are the most common generation failure | irrelevant |
| `no`, `on`, `y` | silently coerced to booleans (the Norway problem) | strings are strings |
| Failure mode of a bad file | a **silently wrong config** | a **parse error** |

**Loud beats silent** — the same principle that decides pool exhaustion, config typing, and the
importer's lossy list. A challenge that silently ships with `state: no` → `state: false` is exactly
the class of bug this project exists to eliminate.

### Why the body is Markdown, not a config string

A challenge description is prose, and prose in a quoted config scalar is what makes every config
format miserable to author. It is doubly true here, because the body is a **template** (below), so it
wants to be a first-class text document rather than an escaped string.

### Why one directory per challenge

Self-contained, `git mv`-able, atomically creatable, and **an agent can be told "produce this
directory"** rather than "surgically edit this shared file". It also gives `instances.jsonl` and the
artifacts an obvious home.

---

## Per-account instances

For flag mode `unique`, each account gets a different **bundle** — a flag, an artifact, and arbitrary
vars — and the body is a Go `text/template` rendered against **that account's** instance. See
[Unique flags](../TARGET-FEATURES.md#unique-flags) for the schema and the assignment semantics.

```jsonl
{"flag":"CTF{a1b2c3}","artifact":"./files/heap.001","vars":{"port":31001,"link":"https://dl.ctf/a1b2"}}
{"flag":"CTF{d4e5f6}","artifact":"./files/heap.002","vars":{"port":31002,"link":"https://dl.ctf/d4e5"}}
```

JSONL, not JSON: it **streams**, it diffs line-by-line in git, and it appends without rewriting the
file — which matters when the pool has 500 entries.

**`flagfishctl` uploads `value_hash = sha256(flag)`, never the plaintext.** The plaintext exists in
the author's repo and inside the artifact; it never reaches the database. That repo is
secret-bearing — keep `instances.jsonl` out of any public mirror.

### The template context — the two structural guarantees

```go
// The ENTIRE data context for a challenge body template. Note what is absent.
type InstanceView struct {
    ArtifactURL string
    Vars        map[string]any    // from challenge_instances.vars
    // NO Flag field. Deliberately. Do not add one.
}
```

1. **Only the caller's own instance is in scope.** Cross-account leakage is not *prevented* — it is
   **unrepresentable**. There is no syntax by which a template can reach another account's vars.
2. **There is no `Flag` field.** An author cannot leak the flag into their own description even by
   accident. This is the difference between a policy and a guarantee.

Render pipeline: `text/template` → markdown → **sanitised** HTML. Never `html/template` over the
markdown source; it would escape the markdown itself.

One consequence to design in rather than discover: for `unique` challenges the detail response
**varies by account**, so any cache must be keyed by `(challenge_id, account_id)`.

---

## Challenge CI: `flagfishctl test` — supported, not required

```toml
[solve]
cmd = "python solve/solve.py $HOST $PORT"
```

```
$ flagfishctl test ./challenges/pwn/heap-overflow
  ✓ frontmatter parses, schema-valid
  ✓ referenced files exist
  ✓ template renders against every instance
  ✓ instances.jsonl: 250 bundles, 250 distinct flags
  ✓ solver produced a flag valid for this challenge     ← PROVABLY solvable
```

That last line is the feature, and it is valuable with zero AI involved. *"The challenge was
unsolvable and nobody found out until 40 teams had burned three hours on it"* happens at every CTF.
Making solvability a **build gate** is, as far as we know, something no platform does.

**Optional by design.** A missing `[solve]` block is a warning, never an error, and `flagfishctl sync`
never blocks on it. Authors who do not want to write a solver are not punished — but the ones who do
get a guarantee.

---

## AI-generated challenges: an agent-friendly CLI, no LLM in the product

We ship **no model, no API key, no prompt, no inference code.** We ship the two things that make any
agent effective, and let the author bring their own:

1. **A published JSON Schema** for the frontmatter. It is the machine-readable contract — editor
   autocomplete, `flagfishctl validate`, and an LLM tool definition, all from one artifact. **The
   schema is the prompt.**
2. **Machine-readable output:** `flagfishctl validate --json` and `flagfishctl test --json`.

```
generate → flagfishctl validate --json → flagfishctl test --json → iterate until green
```

That loop is the entire feature, and it costs nothing to maintain. The **acceptance criterion is
`flagfishctl test`**: a generated challenge is not "plausible", it is **provably solvable or it is
not merged**. That is what turns AI authoring from a gimmick into a workflow, and it is why the CI
gate matters far more than the file format does.

**Why there is no built-in `flagfishctl generate`:** it would demo well and then rot. We would own an
API-key story, prompt maintenance, model deprecation, and per-call cost — inside a CTF platform.
Authors already have a model they pay for; meet them there.

---

## Artifact building is the author's job

The author's own `Makefile` produces `instances.jsonl` and `files/`. `flagfishctl` uploads what is
there.

```
make                 # → instances.jsonl + files/heap.{001..250}
flagfishctl sync     # → uploads the pool
```

**`flagfishctl` stays a sync tool, not a build system.** Shelling out to a declared `[build]` command
would hand us a sandboxing and trust story, and it is the first step down the road toward runtime
instancing, which is deliberately deferred. Keep the boundary sharp.

---

## `flagfishctl` commands

| Command | Notes |
|---|---|
| `flagfishctl new pwn/heap-overflow` | scaffold a directory |
| `flagfishctl validate [--json]` | frontmatter + schema + refs + template renders |
| `flagfishctl test [--json]` | validate, **plus** run `[solve]` and check the flag |
| `flagfishctl sync ./challenges` | idempotent push. `--dry-run` diffs. |
| `flagfishctl pool stats` | the pre-event instance-pool exhaustion gauge |

**`flagfishctl` is a first-party consumer of the public REST API** — the same OpenAPI contract
third-party bots use. That is not incidental: it is what keeps the public API honest. If the CLI needs
an endpoint that does not exist, the API was incomplete.

Two constraints this places on the API:

- **The instance-pool upload must not be multipart.** API-token auth is defined for JSON request
  bodies; a multipart pool upload would be unauthable from a CLI. Use
  `PUT /challenges/{id}/instances` with a `text/plain` (JSONL) or JSON body.
- **`flagfishctl sync` of 200 challenges must not trip the rate limiter.** Limits are per token, not
  per IP, precisely so that a legitimate bulk client is not indistinguishable from an abusive one.

See [The HTTP API](../http-api.md).

---

## Deliberately unresolved

- **`sync` never deletes.** True GitOps would prune challenges absent from the repo; deleting a
  challenge mid-event destroys solves. `sync` is create/update only, and removal is an explicit,
  confirmed command. Loud beats silent, and this one is irreversible.
- **Where per-instance artifacts live before upload.** 250 × a 5 MB VM image is 1.2 GB in a git repo.
  Git LFS or an out-of-band artifact store referenced by URL — this needs a real answer before
  someone tries it at that size.
- **What `flagfishctl test` runs the solver against.** A live deployment (which needs `$HOST`) is the
  honest test; a local compose stack is what CI can actually do. Probably both, selected by flag.
