# Security Policy

flagfish is a scoring system for adversarial competitions. Its users are, definitionally, people who
are good at breaking software and are actively looking for a way to get points they did not earn. We
take that seriously.

> **flagfish is pre-alpha and has not been audited.** Do not run it for anything that matters yet.

## Reporting a vulnerability

**Please do not open a public issue.**

Use GitHub's private vulnerability reporting:
**[Report a vulnerability](https://github.com/starvy/flagfish/security/advisories/new)**
(Security → Advisories → Report a vulnerability).

Please include:

- What breaks, and the impact — *"any team can read another team's flag"* is different from *"an
  admin can crash the worker"*.
- Steps to reproduce, ideally as a failing test.
- The commit you tested against.

We aim to acknowledge within **72 hours** and to ship a fix or a mitigation plan within **90 days**.
If a fix requires a schema migration, that is called out in the advisory — a migration is an operator
action, and an operator who does not know is not protected.

You will be credited in the advisory unless you ask not to be. We do not run a bug bounty.

## What we consider a vulnerability

The obvious ones (authn/authz bypass, injection, RCE, XSS in a challenge body or a page), plus four
classes that are specific to this product and that we treat as **severity-critical**:

| Class | Why it is critical here |
|---|---|
| **Score forgery** | Any path by which an account's score is not `SUM(solves.value) + SUM(awards.value)` over rows it legitimately owns. Includes concurrency: if you can make the hint-unlock double-charge fire, or land two solves for one challenge, that is a vulnerability, not a bug. |
| **Flag disclosure** | Any path that reveals a flag — plaintext, another account's issued flag, or a flag for an unsolved challenge. |
| **Freeze bypass** | Any path that reveals post-freeze standings to a non-admin. This includes `?as_of=` time travel: it is **clamped to the freeze horizon** for non-admins. A leak here decides events. |
| **Anti-cheat evasion** | Any path by which a shared flag is not attributed, or by which `submissions.attributed_account_id` can be made to lie. The anti-cheat properties are advertised as *provable*, so a hole in them is a broken promise, not a missing feature. |

**Out of scope:** anything requiring an already-compromised admin account (admins are trusted by
design — see *audit log* below); vulnerabilities in a challenge *you* authored; DoS by brute-force
submission (that is what the rate limiter is for — but a *bypass* of the rate limiter is in scope);
missing hardening headers with no demonstrated impact.

---

## Design facts a security researcher should know

These are deliberate, load-bearing properties. If you find one of them to be false, that is a
critical bug.

### Flag plaintexts are never stored

For `flag_mode = 'unique'`, the database stores `sha256(flag)` and nothing else:

```sql
CREATE TABLE challenge_instances (
    ...
    value_hash  bytea NOT NULL,   -- sha256(flag). The plaintext NEVER reaches our database.
    ...
);
```

`flagfishctl` hashes locally and uploads the digest. The plaintext exists in the author's repo and
inside the artifact — never in our storage, never in a backup, never in an export.

The submit path for a unique-flag challenge is `sha256(provided)` → one indexed lookup. It never
performs a byte-wise comparison, so **there is no per-byte timing channel to leak**. This is not a
compromise on constant-time comparison; it is stronger than it.

*(Static flags are compared with `subtle.ConstantTimeCompare`. **Regex flags cannot be made
timing-safe** — that is accepted and documented. Regex flags are genuinely used, and dropping them to
close a timing side-channel of that width would be a bad trade. If you rely on flag secrecy against a
timing attacker, do not use regex flags.)*

### The challenge-body template context has no `Flag` field — by construction

For per-account instances, the challenge description is a Go `text/template` rendered against the
requesting account's own instance. The **entire** data context is:

```go
type InstanceView struct {
    ArtifactURL string
    Vars        map[string]any   // from challenge_instances.vars
    // NO Flag field. Deliberately. Do not add one.
}
```

Two guarantees follow, and both are **structural** rather than enforced by care:

1. **A template cannot reach another account's instance.** Cross-account leakage is not *prevented*;
   it is **unrepresentable** — there is no syntax for it.
2. **An author cannot leak their own flag into their own description, even by accident.** The field
   does not exist. This is the difference between a policy and a guarantee.

A PR that adds a `Flag` field to `InstanceView` is a security regression and will be rejected on
sight.

### ⚠️ Treat any import archive as a secret

flagfish can import a CTFd export archive (`flagfish import export.zip`). That archive is a set of
table dumps with no field masking: it contains password hashes, API tokens in plaintext, mail
credentials, and OAuth client secrets. **Handle it as you would a database dump.** Do not paste one
into an issue, including one filed against this project, and do not upload one anywhere you would not
upload a backup.

flagfish's import and export posture:

- **Every API token in an imported archive is dropped.** By policy, not by omission. The count is
  reported (`TOKENS_DROPPED{count: 47, reason: "plaintext credentials are never imported"}`), and
  users mint new tokens. Our schema stores only hashes; importing a plaintext bearer credential would
  put a live credential into a database designed never to hold one.
- **We store `token_hash`, never a token.** A token is shown exactly once, at creation, and can never
  be re-displayed.
- **Our own export is field-masked, and the mask is default-deny.** A table with no mask entry is
  *not exported*; a CI test asserts that every table at goose HEAD appears in the mask table. The
  failure mode is "a table is missing from the export", never "a secret leaked into it". Secret
  config keys, sessions, and password hashes are omitted from the default (`--safe`) profile;
  `--backup` is the full-fidelity profile, and it is a secret.
- **The importer's table list is compile-time.** Nothing from an uploaded archive is ever
  interpolated into SQL — an archive is untrusted input, and it is parsed, not executed.

### The audit log is not tamper-evident — and that is a chosen non-goal

Admin mutations are captured by Postgres triggers (before/after JSONB, actor, IP). The trigger lives
at the *table*, so a new admin endpoint **cannot forget to audit itself** — and neither can a
migration, a console session, or a `psql` fix at 3am.

But the audit log is admin-readable and **admin-deletable**. There is no hash chain and no revoked
`UPDATE`/`DELETE` grant. This is recorded as a deliberate non-goal so that "the audit log isn't
tamper-proof" is never *discovered* as a surprise. **Do not report it as a vulnerability** — do open
an issue if you need the trail to survive a hostile admin, because that is a real requirement and it
would change the design.

`password_hash` is masked out of the audit diff. If you find a table whose trigger captures a
credential into `audit_log.before`/`after`, that **is** a vulnerability.

### Passwords

New passwords are hashed with **Argon2id**. Imported **bcrypt** hashes still verify, and are
transparently rehashed to Argon2id on the user's next successful login — the plaintext is in hand
exactly once, so this is free. The bcrypt population drains to zero without a mass password-reset
blast.

**Changing your password evicts everyone else holding your credentials.** Every other session dies
(each one carries a fingerprint of the password hash it was minted under), and **every API token on
the account is deleted**. That applies to all three paths: the self-service change, the emailed
reset, and an admin forcing a change. It is unconditional and there is no opt-out — the platform
cannot tell routine rotation from a compromise, and the two failures are not symmetric: a surprised
user re-mints a token, while a user whose attacker keeps a token has an ongoing breach with full API
access, flag submission included. The response tells you how many tokens went, because only you can
create replacements. A rehash-on-login is not a password change and revokes nothing.

### Sharing detection is silent, on purpose

When an account submits a flag issued to a *different* account, the submission is accepted as a
normal solve and the player sees an ordinary `Correct!`. Nothing in any response, header, timing, or
notification signals that anything was detected.

This is not an oversight and it is not an information leak — it is the feature. **A detector that
announces itself is not a detector**: rejecting the flag would teach the cheater that the platform
tracks provenance, and they would adapt within minutes. Evidence accumulates; a human decides,
out-of-band.

If you find a way for a player to *learn* that they were flagged — a timing difference, a distinct
error, an SSE event — please report it. That is a real vulnerability against this design.
