# flagfish end-to-end suite

A Playwright suite that boots two real flagfish instances — a **teams-mode** one and a
**users-mode** one, each a static binary against a throwaway Postgres 17 + MinIO — and drives a
browser through them the way a real CTF event would: authoring, registration, teams, the submit hot
path, hints, prerequisites, decay, the scoreboard and its freeze, ops (pause, CSV export, audit,
backup/restore) and the anti-cheat review screens.

## Run it

```sh
web/e2e/scripts/up.sh && (cd web && npm run e2e)
```

First time on a fresh checkout:

```sh
cd web && npm i && npx playwright install chromium
```

Then the two lines above. When you are done:

```sh
web/e2e/scripts/down.sh
```

## The two projects

The account model is fixed at instance setup, so a spec belongs to exactly one instance. Playwright
runs two projects:

| project | account model | base URL                | Postgres | MinIO |
| ------- | ------------- | ----------------------- | -------- | ----- |
| `teams` | teams         | `http://localhost:8019` | `:5588`  | `:9370` |
| `users` | users         | `http://localhost:8020` | `:5589`  | `:9371` |

`up.sh` boots both, idempotently: it builds the SPA (the binary serves it from `go:embed`, so it
must exist first) and the binary once, then for each model brings up throwaway containers, resets the
database to a clean migrated state (via `flagfish migrate`, so the River tables exist), creates the
first admin and starts `serve --with-worker`. Re-running it converges on the same clean slate.

Run one project at a time with `npm run e2e -- --project=teams` (or `--project=users`).

## Notes for anyone extending it

- **Everything imports the harness from `../fixtures`** (`test`, `expect`, `registerPlayer`,
  `login`, team helpers, `uniq`, `submitFlag`, `createChallengeWithFlag`, …). Admin authoring for
  scenario setup goes through `../api`; out-of-band token reads through `../db`.
- **`networkidle` never fires** — the SPA holds an SSE stream open. Wait on concrete elements or
  responses, never on the network going idle.
- The credential rate limiter would trip a login-heavy suite from one IP; `up.sh` lifts the limits
  on the **test** instances only (never in committed product defaults).
- A full restore truncates `sessions`, so the launching admin is logged out — those specs assert
  from a fresh browser context.
- The teams specs `challenge-play-ux` and `profile-team-display` rely on a fixed seed (challenge #1,
  a file-less static challenge worth 250 with one 50-point hint), created once by
  `e2e/global-setup.ts` before any spec runs.
