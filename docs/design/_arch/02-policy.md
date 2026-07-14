# _arch/02 — The Policy Layer

Authorization in flagfish is **one layer, four tiers, one input tuple**. There are no authorization
decorators scattered across handlers, no per-endpoint filter helpers, and no template-level "should
I render this" checks. Every allow/deny decision on every route is made by one pure function over
one struct, and every visibility restriction that must be expressed *inside a query* is one of a
closed, inventoried set of SQL predicates.

This document is the definitive statement of how that works.

---

## 0. The contract

Visibility and authorization decompose into four tiers. The split is not cosmetic: it is drawn
exactly along the line of **what input a check needs**.

| Layer | What it is | Where it runs |
|---|---|---|
| **L1 — Route policy gate** | `Decide(Policy) → Outcome`. Pure function. No DB, no row input. | middleware, before the handler |
| **L2 — Resource guards** | per-row checks that need the *target row* (challenge state, prereqs, ownership, captaincy, module access) | in the handler, before the query |
| **L3 — SQL predicates** | a closed set of WHERE-clause fragments | inside sqlc queries |
| **L4 — Field redaction** | view masks + score/place/solve-count nulling on already-fetched rows | at serialization |

Evaluation order within L1 is **fixed and canonical** (§3.2). Order is not a correctness property —
it never changes whether a caller is allowed, only which denial they are told about — but it *is* a
user-visible property, and a policy layer whose error precedence depends on which handler you hit is
a policy layer nobody can reason about. So we fix it once, in one place, and test it.

---

## 1. The policy input

### 1.1 The struct

```go
package policy

type Mode uint8       // ModeUsers | ModeTeams   — immutable after setup
type Phase uint8      // PhaseBeforeStart | PhaseRunning | PhaseEnded
type Vis uint8        // VisPublic | VisPrivate | VisAdmins | VisHidden(score only) | VisMLC(registration only)
type Surface uint8    // SurfacePublic | SurfaceAdmin

// Event: the config snapshot. Read through an atomic.Pointer — a pointer deref, no cache,
// no round trip. See the config chapter in ../ARCHITECTURE.md.
type Event struct {
	Mode            Mode
	SetupDone       bool
	ChallengeVis    Vis
	ScoreVis        Vis
	AccountVis      Vis
	RegistrationVis Vis
	VerifyEmails    bool
	ViewAfterCTF    bool
	Phase           Phase      // 3-valued, deliberately — see §1.2
	Paused          bool
	FreezeAt        *time.Time // nil = no freeze
	TeamCreation    bool
}

// Principal: everything about the caller, and nothing about any resource. Derived once per
// request from the session *or* the API token — the two credentials produce the same struct,
// which is what makes a ban cover both (§5.10).
type Principal struct {
	Authed              bool
	IsAdmin             bool
	Verified            bool
	Banned              bool
	TeamBanned          bool
	Teamless            bool // team mode && users.team_id IS NULL
	ProfileComplete     bool
	TeamProfileComplete bool
	ForcePasswordChange bool
	UserID, AccountID   int64 // AccountID = user_id | team_id, per Mode
}

// Request: the call site. The freeze exemption lives here rather than on the Principal,
// because it is a property of the endpoint, not of the caller's role (§5.4).
type Request struct {
	Class     RouteClass
	Surface   Surface // Public | Admin
	AdminView bool    // ?view=admin
	Preview   bool    // ?preview
}

type Policy struct{ E Event; P Principal; R Request }
```

`Mode` is a field, not a query, because the account mode is frozen at setup and never changes
afterwards. The boot assertion that enforces it (`user_mode='users'` with a non-empty `teams` table
⇒ refuse to boot; see the [schema chapter](01-schema.md)) is what makes this field trustworthy
enough to cache in a struct.

`Principal` carries **no per-resource state**, and that is load-bearing: it is what lets `Decide` be
a pure function of the request context with no database access at all.

### 1.2 Completeness

The policy layer is only worth having if it is **total**: every route falls into a known class, and
the input tuple is provably sufficient to decide every gate. The argument is a walk over the gates.

Each row is a gate flagfish enforces, and the *only* fields of `Policy` it reads. If any gate needed
a field not in §1.1, the struct would be incomplete and some handler would be forced to make an
authorization decision on its own — which is exactly the failure mode this layer exists to prevent.

| # | Gate | Reads |
|---|---|---|
| 1 | setup not finished → redirect to `/setup` | `E.SetupDone` |
| 2 | ban wall (user ban, team ban) | `P.Authed, P.Banned, P.TeamBanned` |
| 3 | forced password change | `P.Authed, P.ForcePasswordChange` |
| 4 | mode-only routes 404 in the other mode | `E.Mode` |
| 5 | challenge visibility | `E.ChallengeVis, P.Authed, P.IsAdmin` |
| 6 | score visibility | `E.ScoreVis, P.Authed, P.IsAdmin` |
| 7 | account visibility | `E.AccountVis, P.Authed, P.IsAdmin` |
| 8 | registration visibility | `E.RegistrationVis` |
| 9 | authenticated-only routes | `P.Authed` |
| 10 | admin-only routes | `P.IsAdmin` |
| 11 | team membership required | `E.Mode, P.Teamless` |
| 12 | verified email required | `E.VerifyEmails, P.Authed, P.IsAdmin, P.Verified` |
| 13 | during-CTF-time only | `E.Phase, E.ViewAfterCTF, E.Mode, P.IsAdmin, P.Teamless` |
| 14 | complete profile required | `P.Authed, P.IsAdmin, P.ProfileComplete, E.Mode, P.TeamProfileComplete` |
| 15 | paused → no attempts | `E.Paused` **only** — deliberately no `P.IsAdmin` (§5.2) |
| 16 | attempt in team mode needs a team | `E.Mode, P.Teamless, P.IsAdmin` |
| 17 | anonymous attempt rejected | `P.Authed` |
| 18 | admin challenge preview | `P.IsAdmin, R.Preview` |
| 19 | freeze exemption | `R.Surface, R.AdminView, R.Preview, E.FreezeAt` (§5.4) |
| 20 | team creation disabled | `E.TeamCreation` |
| 21 | score/account field nulling | `E.ScoreVis, E.AccountVis, P.Authed, P.IsAdmin` |

Two things fall out of this walk and are worth naming, because a smaller tuple is the obvious wrong
turn here:

**`ctftime` is three-valued, not a boolean.** "Is the CTF running" is not enough to decide row 13.
Before-start and after-end are different denials with different consequences: after the end, the
`view_after_ctf` setting can re-open read-only access, and *before* the start, in team mode, a
teamless user is redirected to team enrollment rather than 403'd — because the useful thing for them
to do in the pre-start window is precisely to join a team. `Phase` (3-valued) plus `ViewAfterCTF` is
the minimal input that decides that cell correctly. Collapsing it to `bool ctftime` silently merges
two outcomes that must differ.

**Profile completeness is a property of the principal, not of a row.** `ProfileComplete` and
`TeamProfileComplete` look like they need a database lookup, and they do — but the lookup is over
*the caller*, not over the resource being accessed, so it resolves once per request alongside the
rest of the principal and stays inside L1's pure input. `ForcePasswordChange` is the same shape.

### 1.3 Why there are four layers and not one

The four checks below cannot be answered from `Policy` alone. They all need the **target resource**:

| Check | Needs |
|---|---|
| challenge `state` (`hidden` → 404, `locked` → 403 on attempt) | the challenge row |
| prerequisites (the caller's solve set ⊇ the challenge's requirements) | the caller's solve set ∩ this challenge's requirements |
| module / audience access | audience membership for this challenge |
| ownership and captaincy (`PATCH /teams/me`, token delete) | the target row's owner |

These are **resource guards (L2)**, evaluated in the handler after the L1 gate and before the query.

Keeping them out of L1 is the whole point of the split. `Decide` is a pure function of the request
context — no database handle, no row, no I/O — and that is what makes it *totally* testable: the
entire L1 surface is a finite cross product of enumerable inputs, so the golden table in §6 can walk
every cell of it. The moment one row-dependent check leaks into `Decide`, the function needs a
`context.Context` and a querier, the table test needs a database, and the exhaustiveness argument
dies. We pay for that purity with a second tier, and it is cheap: L2 checks are few, they are local
to the handler that already loaded the row, and they never need to know about visibility settings or
phases.

L3 exists because some restrictions cannot be a gate at all — "hidden accounts do not appear in
standings" is not a decision about a request, it is a `WHERE` clause. L4 exists because some
restrictions are neither: the row is fetched and returned, but with fields blanked.

### 1.4 Not policy

Deliberately excluded from L1, with reasons:

- **Rate limits** — stateful counters over time, not a decision over `Policy`. Separate middleware.
- **CSRF** — a transport concern, not an authorization one; token auth is exempt by construction
  because it carries no ambient credential.
- **Team disband eligibility** — needs "has this team performed actions", a row aggregate. L2.
- **Caps** (`num_users` / `num_teams` / `team_size`) — a count-then-insert is a race, so the cap is a
  database constraint on the write path, not a read gate (see the
  [schema chapter](01-schema.md)). The *policy* content of caps — who counts, who bypasses — is
  §5.7.

---

## 2. The SQL predicates

Six predicates ship today. A seventh, module-access filtering, lands with audiences (§2.7). That is
the entire set: an inventory test keeps it honest.

sqlc compiles static SQL — it has no fragment composition — so these are copy-pasted WHERE clauses
in `internal/db/queries/*.sql`. Copy-paste is acceptable **only because the set is closed and
small**, and the guard against it drifting is the inventory test described in §6 — not yet built.
Every predicate uses the `@param IS NULL OR …`
idiom, the same technique that lets a single static query serve the dynamic admin filters.

### 2.1 P1 — account not banned, not hidden
```sql
-- non-admin standings, listings, solver lists
AND (@include_masked::bool OR (acct.banned = false AND acct.hidden = false))
```
Applies to public standings, account listings, solver lists and solve counts, and team member lists.
The admin surface passes `include_masked = true`; nothing else does. A banned or hidden account
still *plays* — it is merely absent from every aggregate a player can see (§5.6).

### 2.2 P2 — freeze cutoff (strict `<`)
```sql
AND (@freeze_at::timestamptz IS NULL OR @freeze_exempt::bool OR s.date < @freeze_at)
-- and, on the awards leg of the union:
AND (@freeze_at::timestamptz IS NULL OR @freeze_exempt::bool OR a.date < @freeze_at)
```
The comparison is **strict `<`**: a solve landing exactly on the freeze instant is hidden. A freeze
is an announced cutoff, and "at 18:00:00.000000" must fall on one side of it deterministically; `<=`
would make the boundary depend on clock resolution.

`@freeze_exempt` is **not** `is_admin`. It is computed from the call site — see §5.4, which is the
single most misunderstood rule in this layer.

### 2.3 P3 — zero-value events are excluded
```sql
AND s.value <> 0   -- solves
AND a.value <> 0   -- awards
```
A zero-scoring event must not affect the score (trivially) and must not affect the **tiebreak**
either. The tiebreak key is the account's most recent scoring event; if a zero-point solve or a
zero-point award moved it, an account could reorder itself on the board without earning anything.

Note the left-hand side: `s.value`, the **snapshot stamped on the solve**, not the challenge's
current value. Solve values are stamped at solve time, so the scoring path never joins `challenges`
at all — a challenge whose value is edited later does not retroactively rewrite the board. A
snapshot is a fact; a join to a mutable row is an opinion. (See the
[schema chapter](01-schema.md) for the stamping rules.)

### 2.4 P4 — bracket filter (optional)
```sql
AND (@bracket_id::bigint IS NULL OR acct.bracket_id = @bracket_id)
```
A bracket is a **filter over one global ranking**, not a separate scoring pool: a bracketed board
shows the same points and the same relative order as the global board, with non-members removed. So
it is a `WHERE` clause and never a `GROUP BY`. The bracket join is an outer join, because
`bracket_id` is `ON DELETE SET NULL` — deleting a bracket must not delete its members from the
scoreboard.

### 2.5 P5 — the account-mode column
```sql
-- internal/db/queries/scoreboard.sql
... FROM solves s ... GROUP BY s.user_id ... JOIN users acct ON acct.id = s.user_id   -- users mode
... FROM solves s ... GROUP BY s.team_id ... JOIN teams acct ON acct.id = s.team_id   -- teams mode
```
Account-mode duality touches every gameplay query, and there are exactly two ways to handle it: swap
the column at runtime, or write both queries. We write both — **two named sqlc queries per family**,
each compile-time checked against the schema. A runtime column swap is a string, and a string is not
type-checked by anything.

The attribution itself is **stamped**: `solves.team_id` is written at solve time, not recomputed
from the solver's current team membership. A player who moves teams does not move their past solves
with them (see [the submit hot path](05-hotpath.md)).

### 2.6 P6 — challenge state on the non-admin path
```sql
AND (@is_admin::bool OR ch.state = 'visible')
```
The non-admin read path excludes **both** `hidden` and `locked` challenges from listings and detail.
On the *attempt* path the two states diverge in status code — `hidden` → 404 (it does not exist as
far as you are concerned), `locked` → 403 (it exists and you may not have it yet) — and that
distinction needs the row, so it is L2, not L3.

### 2.7 P7 — module access (with audiences)

Audience-scoped challenges are not in the first release. When they ship, "filter challenges to the
audiences this account belongs to" is a seventh SQL predicate, and `Principal` gains an
`AccountID`-keyed audience set. It is named here so that the "closed set of six" is a fact with an
expiry date rather than a claim that quietly becomes false.

### 2.8 Worked example — public user-mode standings

```sql
-- name: GetUserStandings :many
WITH events AS (
  SELECT s.user_id AS account_id, s.value AS score, s.id, s.date
    FROM solves s
   WHERE s.value <> 0                                                                   -- P3
     AND (@freeze_at::timestamptz IS NULL OR @freeze_exempt::bool OR s.date < @freeze_at)  -- P2
  UNION ALL
  SELECT a.user_id, a.value, a.id, a.date
    FROM awards a
   WHERE a.value <> 0                                                                   -- P3
     AND (@freeze_at::timestamptz IS NULL OR @freeze_exempt::bool OR a.date < @freeze_at)  -- P2
),
sums AS (
  SELECT account_id, SUM(score) AS score, MAX(id) AS id, MAX(date) AS date
    FROM events GROUP BY account_id                                                     -- P5 (user_id leg)
)
SELECT u.id AS account_id, u.name, u.bracket_id, b.name AS bracket_name, sums.score
  FROM users u
  JOIN sums ON sums.account_id = u.id                                                   -- INNER, on purpose
  LEFT JOIN brackets b ON b.id = u.bracket_id
 WHERE (@include_masked::bool OR (u.banned = false AND u.hidden = false))               -- P1
   AND (@bracket_id::bigint IS NULL OR u.bracket_id = @bracket_id)                      -- P4
 ORDER BY sums.score DESC, sums.date ASC, sums.id ASC;
```

The **INNER JOIN is deliberate**: an account with no non-zero scoring event is *absent* from the
board, not shown at 0. Registering does not put you on the scoreboard; scoring does. A LEFT JOIN
here silently changes the product, so it is pinned by a test (§6, §5.11).

The `ORDER BY` is the whole tiebreak: points descending, then earliest last-scoring-event, then id.
Ties are broken toward whoever got there first.

P6 does not appear in this query because standings never join `challenges` — that is the direct
benefit of stamping solve values (§2.3). P6 lives only on the challenge read path.

---

## 3. L1 — the route policy gate

### 3.1 Decision table

`A` = allow · `AR` = AuthRequired · `403` · `404` · `→X` = redirect to X.
Rows are route classes; the table is the *whole* L1 surface.

| Route class | Vis gate | Authed | Verified | Team | Phase | Paused | Extra |
|---|---|---|---|---|---|---|---|
| `Index`, `Pages` | — | — | — | — | — | — | — |
| `Register` | Registration: `private`→404 · `mlc`→404 (§5.9) | already-authed →`/challenges` | — | — | — | — | caps §5.7 |
| `Login`, `Reset`, `Confirm` | — | — | — | — | — | — | — |
| `ChallengeList`/`Detail` | Challenge | `private`→AR · `admins`+authed→403 · `admins`+anon→AR | ✔ | teams-mode & teamless → 403 | ✔ | — | L2: state, prereqs |
| `ChallengeAttempt` | Challenge | **must be authed** (403) | ✔ | teams-mode & teamless → 403 | ✔ | **403 `paused` — no admin exemption** | admin `?preview` short-circuits *before* pause |
| `HintUnlock` / `SolutionUnlock` | — | ✔ (403) | ✔ | — | ✔ | **not checked (§5.3)** | L2: affordability |
| `Scoreboard` | Account **and** Score | `private`→AR · `hidden`→403 · `admins`→404 | — | — | **not gated** | — | freeze via `R.Surface` |
| `AccountList`/`Detail` | Account | `private`→AR · `admins`→404 | — | — | — | — | L3 P1: banned/hidden→404 |
| `AccountSelf` (`/me`) | — | ✔ | — | `/teams/me` needs team | — | — | own score always live (§5.8) |
| `TeamEnrollment` (`/team`) | — | ✔ | — | — | — | — | teamless → enrollment page |
| `TeamCreate` | — | ✔ | — | already in team → 403 | — | — | `team_creation` off → 403 |
| `Tokens` | — | ✔ | ✔ | — | — | — | L2: ownership |
| `Admin*` | — | **IsAdmin** else 403 | — | — | — | — | `Surface = Admin` |
| `SSE` | — | ✔ (403) | — | — | — | — | banned already 403'd |
| `Setup` | — | — | — | — | — | — | 404 once `SetupDone` |

Read the `Vis gate` column carefully: there is a **deliberate asymmetry**. `admins` visibility yields
**403** on challenges but **404** on scores and accounts. For accounts and scores, the *existence* of
the data is the secret — knowing there is a scoreboard you cannot see is itself a leak of the event's
shape. For challenges it is not: everyone knows a CTF has challenges, and a 403 tells a legitimate
player something useful ("this is gated, not broken"). This asymmetry is named and tested (§5.5)
precisely because it looks like an inconsistency.

Two gates apply globally, before the table: **banned** (403 on every route except static theme
assets) and **forced password change** (redirect on every route except theme assets, logout, and the
reset endpoint itself — otherwise the user could not comply with the redirect).

### 3.2 Canonical evaluation order

```
1. SetupDone           → 302 /setup
2. authn (session | token)          ← the token path resolves the Principal fully (§5.10)
3. Banned | TeamBanned → 403        ← therefore covers token auth
4. ForcePasswordChange → 302 /reset_password/<t>
5. Mode                → 404        (route doesn't exist in this mode)
6. Surface visibility  → per table
7. Authed requirement  → AuthRequired
8. Verified            → 403 / 302 confirm
9. ProfileComplete     → 302 settings | 403 (team)
10. Team requirement   → 403 / 302 enrollment
11. Phase              → 403 / 302 enrollment (teamless, pre-start)
12. Paused             → 403  (attempt only)
--- L1 ends. L2 resource guards run in the handler. ---
```

The order is a **single fixed sequence for every route class**. It changes only which error a denied
caller sees, never allow/deny — which is exactly why it must be pinned: a property that no test
naturally covers, and that drifts the moment someone reorders two checks, is a property that will
drift. The table test in §6 asserts the precedence, cell by cell.

The ordering also encodes a security preference: identity-level gates (banned, password change) run
*before* content gates (visibility, phase), so a banned user never learns anything about the event's
configuration from the error they get.

### 3.3 The code

```go
type Outcome struct {
	Allow    bool
	Status   int    // 403 | 404 | 0
	Redirect string // "" | "/login" | "/confirm" | "/settings" | "/team" | "/setup"
	Reason   Reason // named, for tests + the RFC 7807 `type`
}

var (
	Allow        = Outcome{Allow: true}
	AuthRequired = Outcome{Status: 403, Redirect: "/login", Reason: ReasonAuthRequired}
	NotFound     = Outcome{Status: 404, Reason: ReasonNotFound}
	// … one var per Reason; the transport maps Redirect→302 for HTML, ignores it for JSON.
)

func Decide(p Policy) Outcome {
	e, pr, r := p.E, p.P, p.R

	if !e.SetupDone && r.Class != ClassSetup {
		return Outcome{Redirect: "/setup", Reason: ReasonSetupIncomplete}
	}
	if pr.Authed && (pr.Banned || pr.TeamBanned) && r.Class != ClassThemeAsset {
		return Outcome{Status: 403, Reason: ReasonBanned} // §5.10: token auth included
	}
	if pr.Authed && pr.ForcePasswordChange && !r.Class.ExemptFromPasswordChange() {
		return Outcome{Redirect: "/reset_password", Reason: ReasonPasswordChangeRequired}
	}
	if !r.Class.AvailableIn(e.Mode) {
		return NotFound
	}

	for _, v := range r.Class.VisibilityGates() { // {Challenge}, {Score}, {Account,Score}, {Registration}
		if o := checkVis(v, e, pr); !o.Allow {
			return o
		}
	}
	if r.Class.RequiresAuth() && !pr.Authed {
		if r.Class == ClassChallengeAttempt {
			return Outcome{Status: 403, Reason: ReasonAuthenticationRequired}
		}
		return AuthRequired
	}
	if e.VerifyEmails && pr.Authed && !pr.IsAdmin && !pr.Verified && r.Class.RequiresVerified() {
		// The `pr.Authed &&` conjunct is deliberate, not an oversight — §5.1.
		return Outcome{Status: 403, Redirect: "/confirm", Reason: ReasonUnverified}
	}
	if r.Class.RequiresCompleteProfile() && pr.Authed && !pr.IsAdmin {
		if !pr.ProfileComplete {
			return Outcome{Redirect: "/settings", Reason: ReasonIncompleteProfile}
		}
		if e.Mode == ModeTeams && !pr.Teamless && !pr.TeamProfileComplete {
			return Outcome{Status: 403, Reason: ReasonIncompleteTeamProfile}
		}
	}
	if r.Class.RequiresTeam() && e.Mode == ModeTeams && pr.Teamless {
		return Outcome{Status: 403, Redirect: "/team", Reason: ReasonTeamRequired}
	}
	if r.Class.TimeGated() && !pr.IsAdmin {
		switch e.Phase {
		case PhaseEnded:
			if !e.ViewAfterCTF {
				return Outcome{Status: 403, Reason: ReasonCTFEnded}
			}
		case PhaseBeforeStart:
			if e.Mode == ModeTeams && pr.Teamless {
				// Pre-start, the useful action for a teamless player is to join a team.
				return Outcome{Redirect: "/team", Reason: ReasonTeamRequired}
			}
			return Outcome{Status: 403, Reason: ReasonCTFNotStarted}
		}
	}
	// Pause has NO admin exemption, and gates ONLY the attempt class. Do not "fix" this — §5.2, §5.3.
	if r.Class == ClassChallengeAttempt && e.Paused && !r.Preview {
		return Outcome{Status: 403, Reason: ReasonPaused}
	}
	return Allow
}

func checkVis(kind VisKind, e Event, pr Principal) Outcome {
	switch e.visFor(kind) {
	case VisPublic:
		return Allow
	case VisPrivate:
		if pr.Authed { return Allow }
		return AuthRequired
	case VisHidden: // score only
		if pr.IsAdmin { return Allow }
		return Outcome{Status: 403, Reason: ReasonScoresHidden}
	case VisAdmins:
		if pr.IsAdmin { return Allow }
		if kind == VisChallenge { // 403 if authed, login if not — §5.5
			if pr.Authed { return Outcome{Status: 403, Reason: ReasonAdminsOnly} }
			return AuthRequired
		}
		return NotFound // score + account hide existence — §5.5
	case VisMLC: // registration only — §5.9
		return NotFound
	}
	return NotFound
}
```

`Outcome` carries **both** a status and a redirect, and the transport adapter picks: JSON and SSE
take the status (Huma, RFC 7807 problem documents — see the [API chapter](03-api.md)); HTML takes
the redirect if there is one, else the error page. The "is this a browser or an API client" fork
exists exactly once, in the adapter, rather than inside every gate.

**Freeze is not in `Decide`.** It is not an allow/deny decision; it is a query parameter. §5.4.

---

## 4. L4 — field redaction

The same entity is serialized differently to different callers: a user sees another user's public
profile, their own profile with their email on it, and an admin sees the ban/hidden/verified flags
too. That is a view mask, and there are two wrong ways to do it.

### 4.1 The honest tradeoff with Huma

Huma derives OpenAPI from Go types, and the TypeScript client is generated from that OpenAPI
document — so anything that destroys the response type destroys the drift detection we adopted Huma
to get.

| | one struct + reflection mask → `map[string]any` | N hand-written structs | **declare-once + generate** |
|---|---|---|---|
| Huma typing | **lost** — schema becomes untyped | full | full |
| Generated TS client | breaks — no client types | works | works |
| Single source of truth | yes | **no** — N places to drift | yes |
| Cost | none | N structs, forever | one ~200-line generator |

Reflection-to-map is **disqualified**: the public REST API is a first-class product surface, and an
untyped response schema means the frontend loses compile-time knowledge of what the server returns.
So the question is not *whether* the view structs exist — it is *where they come from*.

### 4.2 The design

**Step 1 — delete half the problem by splitting on path.** A response shape that varies by the
caller's *role* on a single path cannot be typed in OpenAPI (it is a `oneOf` at best, and a `oneOf`
generates a union the client must narrow at runtime). flagfish already has a separate admin
namespace. So: **the admin view moves to the admin path.** `GET /api/v1/users/{id}` returns the
public shape, always. `GET /api/v1/admin/users/{id}` returns the admin shape. "Who is calling" becomes
"which URL", which is a thing OpenAPI can express.

That eliminates every `admin` view from the shared surface. What legitimately remains is the
caller-dependent split that is *not* a role: `user` vs `self` (users, teams, submissions) and
`locked` vs `unlocked` (hints, solutions).

**Step 2 — declare the masks once; generate the structs.**
```go
//go:generate maskgen -in entities.go -out entities_views.go

type User struct {
	ID          int64   `json:"id"          views:"user,self,admin"`
	Name        string  `json:"name"        views:"user,self,admin"`
	Website     *string `json:"website"     views:"user,self,admin"`
	Country     *string `json:"country"     views:"user,self,admin"`
	Affiliation *string `json:"affiliation" views:"user,self,admin"`
	BracketID   *int64  `json:"bracket_id"  views:"user,self,admin"`
	OAuthID     *int64  `json:"oauth_id"    views:"user,self,admin"`
	TeamID      *int64  `json:"team_id"     views:"user,self,admin"`
	Fields      []Field `json:"fields"      views:"user,self,admin"`
	Email       string  `json:"email"       views:"self,admin"`
	Language    *string `json:"language"    views:"self,admin"`
	Banned      bool    `json:"banned"      views:"admin"`
	Hidden      bool    `json:"hidden"      views:"admin"`
	Verified    bool    `json:"verified"    views:"admin"`
	Type        string  `json:"type"        views:"admin"`
	// The password hash is never serialized in any view — it is load-only.
}
// generates: type UserViewUser struct{…}; type UserViewSelf struct{…}; type UserViewAdmin struct{…}
```
The generator is a `reflect` walk plus `text/template`; the emitted file is committed, and CI fails on
`git diff --exit-code` — the same discipline that keeps `openapi.yaml` honest.

**The same mask governs writes.** The generated `…ViewSelf` struct is *also* the PATCH input type, so
a field absent from the self view cannot be written by its owner. `change_password` is not in the
`self` view, therefore a user cannot self-set it — and that is a **compile error**, not a runtime
check somebody can forget to add to a new endpoint. Mask-as-input is the cheapest mass-assignment
defense there is.

### 4.3 The null-out layer

Score, place and solve-count nulling is a *different mechanism* from the view mask and must stay
separate. The row is computed and returned; specific fields on it are blanked.

- `score` and `place` become **`null`** when scores are not visible to this caller. The field is
  still present in the JSON.
- Challenge **solve counts** become `null` when scores *or* accounts are invisible — a solve count is
  an aggregate over accounts, so it leaks under either setting.
- The visibility predicates themselves: `public → true`, `private → authed`, `admins → is_admin`,
  `hidden → false` (score only).

```go
// Redactor is derived from the SAME Policy that fed Decide(). One source, two consumers.
type Redactor struct{ ScoresVisible, AccountsVisible bool }

func NewRedactor(p Policy) Redactor {
	return Redactor{
		ScoresVisible:   visible(p.E.ScoreVis, p.P),
		AccountsVisible: visible(p.E.AccountVis, p.P),
	}
}

// Nullable score/place: pointers, so `null` is representable and distinct from 0.
func (r Redactor) Account(a *AccountView) {
	if !r.ScoresVisible {
		a.Score, a.Place = nil, nil
	}
}

func (r Redactor) Challenges(cs []ChallengeView) {
	if r.ScoresVisible && r.AccountsVisible {
		return
	}
	for i := range cs {
		cs[i].Solves = nil // null, not 0
	}
}
```

`Score` / `Place` / `Solves` are `*int` in the response structs **because the wire format is `null`,
not `0` and not absent**. A hidden score that serializes as `0` is not hidden — it is a lie, and it
is a lie the frontend will happily render as a scoreboard. This is the exact class of silently-wrong
result that typed, compile-checked queries exist to prevent, and it deserves the same care at the
serialization boundary.

**Row-state-driven redaction** — hints as `locked` / `unlocked` depending on whether the caller
unlocked them or the CTF has ended, solutions gated on `state = 'visible'` or on having solved,
prerequisite-gated challenges shown anonymized as `"???"` — is redaction whose *input is a row*. It
selects which generated view struct to return; it needs no new mechanism. Its selection predicate is
an L2 resource guard.

---

## 5. Named policies

The rules below are the ones that will, at some point, look like bugs to a competent engineer reading
the code cold. Each has a shape that invites a "fix": a missing admin exemption, a check applied to
one route class and not its neighbours, a conjunct that looks redundant. Every one of them is a
deliberate decision about how the game works, and undoing any of them changes the product.

So each is a **named policy with a named test**, registered in `internal/domain/policy`. A comment
that says "this is deliberate" is indistinguishable from a comment somebody left on a bug; a failing
test named after the rule is not. None may be changed without a product decision.

### 5.1 `PolicyUnverifiedStricterThanAnonymous`

**The rule.** With email verification on and `challenge_visibility = public`, an **anonymous** visitor
sees the challenge list, but an **authed-but-unconfirmed** user gets 403. Being logged out is strictly
*more* permissive than being logged in and unverified.

**Why.** The two settings answer different questions. `challenge_visibility` answers "is this event's
content public?" — and if the organizer says yes, it is public, to everyone, including people with no
account. Email verification answers "may this *account* participate?" It is a gate on the account, not
on the content, and an unverified account is an account whose gate has not opened yet. Making
verification apply to anonymous callers would mean an organizer who turns on email verification
silently makes a public event private, which is not what they asked for.

**What breaks if you "fix" it.** Deleting the `pr.Authed &&` conjunct does not tighten anything for
anonymous visitors — they were already allowed by the visibility setting. It *loosens* the gate by
changing what "unverified" means, or it makes a public event unbrowsable. Neither is the ask.

**Test:** `TestUnverifiedMoreRestrictedThanAnonymous` — anonymous 200, unverified 403, on the same path.

### 5.2 `PolicyPauseHasNoAdminExemption`

**The rule.** While the CTF is paused, **no one** — not even an admin — can record a solve through the
attempt endpoint.

**Why.** A pause is a statement about the game clock: *nothing happened during this window*. It exists
because something is broken — a challenge is down, infrastructure is on fire, the scoring is wrong —
and the organizer needs the board to stop moving while they fix it. An admin-exempt pause is not a
pause; it is a pause with a hole in it, and the first thing that goes through the hole is an admin
"just testing" a flag on a live challenge and stamping a solve at a timestamp the players could not
have reached. Admins can still *read* everything; they cannot *score*.

**What breaks if you "fix" it.** Adding `!pr.IsAdmin` to the pause branch makes the paused window
non-atomic: solves exist inside a window the scoreboard says was empty.

**Preserved by:** the pause branch in `Decide` reads `e.Paused` and `r.Preview` only — never
`pr.IsAdmin`.
**Test:** `TestPauseBlocksAdminAttempt`.

### 5.3 `PolicyPauseDoesNotBlockUnlocks`

**The rule.** Pause gates **exactly one** mutation point: the attempt. Hint and solution unlocks still
work — and still charge — while paused. A player can burn score on hints for a challenge they cannot
currently attempt.

**Why.** Pause freezes the *scoring* clock, and a solve is the only thing that changes an account's
standing relative to everyone else's in a way the pause is meant to prevent. An unlock is a
self-inflicted cost the player chooses to pay; blocking it does not protect anyone, and blocking it
mid-pause would strand a player who was midway through reading a hint chain. It is also genuinely
useful: a pause is when players catch up on reading.

**What breaks if you "fix" it.** Extending the pause gate to the unlock classes changes the
economics of a paused window and will surprise players who budgeted hint spend around it. It is a
product decision, not a bug fix.

**Preserved by:** `r.Class == ClassChallengeAttempt` in the pause branch. It looks exactly like a
missing check; it is not.
**Test:** `TestPausedHintUnlockStillCharges`.

### 5.4 `PolicyFreezeExemptionIsCallSiteDriven` — the headline

**The rule.** The freeze exemption is a property of **the endpoint**, not of the caller's role. The
public scoreboard is frozen **for everyone, including admins**. The admin scoreboard is live. "Admins
always see live data" is false, and it is false on purpose.

**Why.** An admin must always be able to see *exactly what the players see*. During the last hour of a
CTF, the frozen board is the single most consequential artifact in the event — every question in
support chat is about it — and an organizer who cannot load the same page a player is looking at
cannot answer any of those questions. If the exemption keyed off `is_admin`, an admin would never
again be able to see the public board; the state everyone else is arguing about would be the one
state they cannot render. The live data does not disappear: it is on the admin surface, which exists
for precisely that purpose. One URL for what the players see, one URL for the truth.

Making the exemption a function of the call site also makes it **auditable**. Role-driven exemptions
spread: every new endpoint with a scoreboard-shaped query grows an `if is_admin` and nobody can
enumerate them. Here there is one function, and its arms *are* the inventory.

| Endpoint | Freeze exemption | Effect |
|---|---|---|
| public scoreboard (page and API) | `Surface == Admin` → false | **frozen**, for everyone including admins |
| scoreboard detail / top-N | false | **frozen**, unconditionally |
| admin scoreboard | true | live |
| statistics, progression | true | live |
| admin CSV export | true | live |
| challenge **list** solve counts | `?view=admin` | live only on the explicit admin view |
| challenge **detail** solve count | false | **frozen, even for admins** |
| per-challenge solves list | `IsAdmin && !preview` | the one genuinely role-driven surface; `?preview` inverts it so an admin can see the frozen view |

**Preserved by:** the freeze exemption is computed in exactly one function, from the call site first,
and from the role only on the one surface where the role is genuinely the right input:

```go
// The ONLY place the freeze exemption is computed. pr.IsAdmin appears in exactly one arm.
func FreezeExempt(p Policy) bool {
	pr, r := p.P, p.R
	switch r.Class {
	case ClassScoreboard, ClassScoreboardDetail, ClassAccountDetail:
		return r.Surface == SurfaceAdmin // public surface = frozen, for EVERYONE incl. admins
	case ClassChallengeList:
		return r.AdminView               // the explicit ?view=admin, not is_admin()
	case ClassChallengeDetail:
		return false                     // frozen even for admins
	case ClassChallengeSolves:
		return pr.IsAdmin && !r.Preview  // role-driven, inverted by ?preview
	case ClassStatistics, ClassExport, ClassAdminScoreboard:
		return true
	}
	return false
}
```

**What breaks if you "fix" it.** Replacing the switch with `return pr.IsAdmin` makes the public
scoreboard unviewable by the people running the event, and does it silently — there is no error, the
page just shows different numbers than every player sees.

**Test:** `TestFreezeExemptionIsCallSiteDriven`.

### 5.5 `PolicyAdminsOnlyHidesExistenceForAccountsNotChallenges`

**The rule.** `visibility = admins` yields **403** on challenges but **404** on scores and accounts.

**Why.** Different information-disclosure targets. For scores and accounts, the existence of the data
*is* the secret an organizer is buying when they set `admins`: a 403 on `/scoreboard` confirms there
is a scoreboard, that it has data, and that someone is on it. For challenges, existence is not a
secret — the event obviously has challenges — and a 403 tells a legitimate player the useful truth
("gated, not broken") instead of sending them to file a bug report about a 404.

**What breaks if you "fix" it.** Making them uniform picks one of two losses: either the account
surface starts confirming what it is meant to hide, or every gated-challenge access looks like a
server fault.

**Preserved by:** the `kind == VisChallenge` branch in `checkVis`.
**Test:** `TestAdminsOnlyVisibilityStatusCodes` — table-driven over all four settings × three roles.

### 5.6 `PolicyHiddenIsNotBlocked`

**The rule.** The ban wall blocks `banned`, never `hidden`. A hidden account authenticates, plays and
solves normally; it is merely absent from listings, standings, solver lists and caps.

**Why.** `hidden` and `banned` are opposite tools. Ban means "you are out". Hidden means "you are in,
but you do not appear" — it is what organizers, sponsors, and the setup admin account use to play or
test without polluting the board. A hidden account that could not solve would be useless for exactly
the thing it exists for: verifying a challenge is solvable on the live instance.

**Preserved by:** `Principal` has **no `Hidden` field**. Hiddenness is an L3 SQL predicate (P1) and
can never reach an L1 decision. The absence of the field *is* the policy — you cannot accidentally
gate on a bit that is not in the struct.

**Test:** `TestHiddenPrincipalIsNotAThing`.

### 5.7 `PolicyCapsExcludeMaskedAccountsAndAdminsBypass`

**The rule.** `num_users` / `num_teams` count only non-banned, non-hidden accounts, so the hidden setup
admin never consumes a slot. Admin creation paths bypass the caps and `team_size` entirely.

**Why.** A cap is a statement about *competitors*, not about rows. If the setup admin ate a slot, an
organizer who set `num_users = 100` would get 99 players and no explanation. Banned accounts are the
same argument in reverse: kicking someone out must give their slot back, or the cap silently ratchets
down every time a cheater is removed. And an admin creating an account is an override by definition —
an admin who cannot add the 101st player when the venue found another seat is an admin fighting their
own tool.

**Preserved by:** `CountsTowardCap` and `BypassesCaps`, named predicates rather than inline `WHERE`
fragments, so both rules are visible at every call site. The cap itself is enforced by a database
constraint, not a count-then-insert (see the [schema chapter](01-schema.md)).
**Test:** `TestCapsIgnoreHiddenAndBannedAndAdminsBypass`.

### 5.8 `PolicyOwnScoreAlwaysVisible`

**The rule.** `/users/me` (and `/teams/me`) returns a **live** score — freeze-ignoring,
visibility-ignoring — while `place` stays gated by the normal score-visibility rules.

**Why.** The asymmetry is exactly right, because the two fields leak different things. A player can
sum their own solves on paper; hiding their own score buys nothing and just makes the client lie to
them. Their **rank** is different: it is a function of everybody else's scores, and it is precisely
the thing a freeze exists to conceal. So: your own points, always; your position, only when the board
is visible.

**What breaks if you "fix" it.** Gating own-score behind the freeze makes a player's own profile
disagree with the solves list on the same page. Ungating `place` leaks the frozen board one player at
a time.

**Preserved by:** `Redactor.Self` nulls `Place` and never `Score`.
**Test:** `TestOwnScoreLiveUnderFreezeAndHiddenScores`.

### 5.9 `PolicyRegistrationMLCTreatedAsPrivate`

**The rule.** When registration is configured for an external identity provider, the built-in
registration **form route returns 404**, exactly as `private` does. Accounts are created only through
the provider's OAuth callback.

**Why.** If the organizer has delegated account creation to an external identity provider, a working
local signup form is a hole in that delegation: it mints accounts that bypass the provider's
membership check entirely. So the form route must not exist. 404 rather than 403 for the same reason
as `private` — the form's absence is the whole point; there is nothing to tell the caller about it.

The failure mode this guards against is a visibility value with no explicit arm falling through the
gate and returning "no decision", which any sane framework turns into a 500. Every arm of the
visibility switch is exhaustive, and this one is the arm that would otherwise be missing.

**Preserved by:** the `VisMLC` arm of `checkVis`.
**Test:** `TestRegistrationMLC404sTheFormRoute`.

### 5.10 `PolicyBanCoversTokenAuth`

**The rule.** A ban covers **every credential**. Session cookie, API token, anything else we ever add:
a banned user is denied on all of them.

**Why.** A ban that only stops the browser is not a ban. The obvious way to get this wrong is to make
the ban wall a session-level check — it reads so naturally as one — and then wire token authentication
in *after* it, or in a different middleware, or inside the token lookup where nobody thinks to add a
ban check. The result is a banned user who keeps full API access **including flag submission**, which
is not a quirk to preserve. It is an authorization bypass, and it is one of the reasons this layer is
one layer.

**Preserved by construction:** the `Principal` is resolved from *either* credential **before** `Decide`
runs, and `Decide` reads `pr.Banned` without knowing or caring which credential produced it. There is
exactly one ban check and exactly one principal, so the bug is not reachable by accident — you would
have to add a second authentication path that skips principal resolution, and there is nowhere to put
one.

**Test:** `TestBannedPrincipalIsRejectedRegardlessOfCredential`.

### 5.11 `PolicyZeroValueEventsAbsentFromStandings`

**The rule.** P3 excludes zero-value solves and awards, and the standings join is INNER. Consequence:
an account whose *only* solves are zero-valued produces no standings row and is **absent from the
scoreboard entirely** — not shown at 0 points.

**Why.** The scoreboard ranks competitors by what they have earned. An account that has earned nothing
has no rank, and an event with 4,000 registrations and 200 solvers should render 200 rows, not 4,000
rows of which 3,800 are zeroes. "Shown at 0" is also not a neutral display choice: it means every
zero-point event silently becomes a tiebreak-moving one, since the account now has a
last-scoring-event timestamp.

**What breaks if you "fix" it.** Switching the INNER JOIN to a LEFT JOIN changes the product without
changing a single line of policy code — which is why it is pinned by a test that asserts *absence*,
not just row content.

**Preserved by:** the zero-value filter plus the INNER JOIN in the standings query.
**Test:** `scoring.TestZeroOnlySolverAbsentFromBoard`.

### 5.12 `PolicyManualGradeBackdatesSolve`

**The rule.** When an admin manually marks a submission correct, the resulting solve is stamped with
`date = submission.date` — the **original** submission timestamp, not the moment of grading.

**Why.** A manual grade is a correction of a judgment, not a new event. The player submitted at 14:02;
the flag was mistyped in the challenge config; the admin fixes it at 19:40. The player earned that
solve at 14:02, and the tiebreak — which rewards getting there first — must say so. Grading at
wall-clock time would punish the player for the organizer's bug, and would do it in the one field that
decides ties.

**The consequence, which is real:** a manually graded solve can land *behind* the freeze cutoff if the
original submission predates the freeze, and it moves the account's tiebreak key backwards. It is the
only operation in the system that can move a row across the freeze boundary after the fact. That is
accepted, not overlooked — the alternative is worse.

This is a **write-path** policy. It is named here because it is the one thing that can violate the
otherwise-monotone assumption behind P2.

**Preserved by:** `SolveDateForManualGrade`, a named function, so the choice is visible at the call
site rather than implicit in whichever timestamp happened to be in scope.
**Test:** `TestManualGradeLandsPreFreeze`.

---

## 6. Test surface

The policy layer's tests **are** the specification. Two kinds:

1. **`TestDecideTable`** — a golden table over `RouteClass × {anon, unverified, verified, teamless,
   banned, team-banned, admin} × {4 visibility settings} × {3 phases} × {paused} × {2 modes}`. This is
   only tractable because `Decide` is pure: no database, no fixtures, the whole cross product runs in
   milliseconds. The table is checked in; a diff to it is a product change and must be reviewed as one.
   Every §5 policy has a named row.
2. **A predicate inventory test — planned, not yet written.** It should parse
   `internal/db/queries/*.sql` and assert that every query tagged `-- scoring` carries P1, P2, P3 and
   (for standings) an INNER JOIN to the sums CTE. That is what would keep the closed predicate set
   honest as queries are added: a new scoreboard query that forgets the freeze predicate should fail
   the build, not the CTF. Until it exists, the closed set is maintained by review.

The named policies of §5 each have their own test, listed with the policy. Those tests are the reason
the policies are safe to leave in the code: they are the comment that cannot rot.

---

## 7. Notes on the edges

Two behaviors worth stating explicitly, because they are the ones people ask about.

- **`require_complete_profile` is enforced on the API, not just the browser.** It gates the challenge
  list, the challenge detail *and* the attempt endpoint — every route class that carries
  `requiresProfile`. A required field that a player can skip by using the API instead of the UI is an
  authorization gap dressed up as a UX nudge; if an organizer marks a field required, an account
  without it does not play, through any surface.

- **Anonymous callers have an empty solve set.** L2 prerequisite checks take the caller's solve set,
  and for an anonymous viewer that set is empty — so on a fully public challenge list, an anonymous
  visitor sees prerequisite-gated challenges as locked and anonymized (`"???"`), exactly as a
  logged-in player who has not met the prerequisites does. This is the correct reading: prerequisites
  gate content, not identity, and the anonymous viewer has met none of them.
