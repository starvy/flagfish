# Deploying flagfish with docker compose

`deploy/compose.yaml` runs a competition-shaped stack on one host: the single static
`flagfish` binary, Postgres 17, and MinIO for challenge files. It is the reference for how
the pieces fit — migrations before serving, a bucket before uploads — and a fine way to
run a small event behind a TLS reverse proxy.

## Topology

```
postgres ──healthy──┐
                    ├─► migrate (one-shot: `flagfish migrate`) ──done──┐
minio ──healthy──► minio-init (one-shot: `mc mb`) ──done────────────┼─► flagfish (serve --with-worker) :8000
```

- **postgres** — `postgres:17`, named volume `pgdata`, `pg_isready` healthcheck. Not
  published: only the compose network reaches it.
- **minio** — object storage, named volume `miniodata`, `mc ready` healthcheck.
- **minio-init** — runs once, creates the bucket the app writes to (`mc mb
  --ignore-existing`), then exits. Idempotent, so re-running `deploy-up` is safe.
- **migrate** — runs `flagfish migrate` once and must exit 0 before the app starts.
  Migrations take a `pg_advisory_lock`, so this is a single one-shot job rather than a
  step baked into every replica: scale `flagfish`, keep `migrate` a job. A failed migration
  blocks the rollout instead of letting a half-migrated app take traffic.
- **flagfish** — `serve --with-worker` (API + job worker in one container). Its
  healthcheck is the binary probing itself (`flagfish healthcheck` GETs `/healthz`), because
  the distroless image has no shell or curl.

`depends_on` with `condition: service_healthy` / `service_completed_successfully` wires
the ordering: the app never starts before the database is up, the schema is migrated, and
the bucket exists.

## Run it

```sh
cp deploy/.env.example deploy/.env      # then edit — set real secrets
task deploy-up                          # build images, start everything, wait for healthy
# ... run the event ...
task deploy-down                        # stop (keep data)   |   task deploy-down-clean (wipe volumes)
```

`deploy-up` refuses to run without `deploy/.env`. Under the hood it is
`docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build --wait`.

The secret values in `deploy/.env.example` are **empty on purpose**, not `change-me`.
Compose's `${VAR:?}` rejects an empty value, so a copied-and-forgotten file stops the stack
at boot rather than running a public CTF on a password that ships in this repository. Keep
that property: never commit a real value into the example file.

One trap: `task e2e` writes a `deploy/.env` with throwaway secrets if none exists, so a box
that has run the E2E suite already has a `deploy/.env` — and `task deploy-up` will happily
boot on it. Check the file before you deploy anything you care about.

## Environment

Everything is set in `deploy/.env` (git-ignored; `deploy/.env.example` is the template).
The app's `DATABASE_URL` and `FLAGFISH_S3_*` are **derived** in the compose file from these,
so you set secrets once:

| Variable | Meaning |
| --- | --- |
| `POSTGRES_PASSWORD` | **required** — Postgres password (and half the app DSN) |
| `POSTGRES_USER` / `POSTGRES_DB` | Postgres role / database name (default `flagfish`) |
| `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` | **required** — MinIO credentials, reused as the app's S3 keys |
| `FLAGFISH_S3_BUCKET` | bucket for challenge files (default `flagfish-files`) |
| `FLAGFISH_HTTP_ADDR` | host port to publish the app on (default `8000`) |
| `FLAGFISH_TRUSTED_PROXIES` | proxy CIDRs whose `X-Forwarded-For` is believed — **set this behind a proxy** |
| `FLAGFISH_SECURE_COOKIES` | default `true`. Only set it `false` if you are deliberately serving plain HTTP |
| `FLAGFISH_MAX_UPLOAD_BYTES` | largest multipart upload (default 32 MiB); every other body is capped at 1 MiB |
| `FLAGFISH_RATE_LIMIT` / `FLAGFISH_RATE_WINDOW` | per-caller request budget (default `60` per `1m`) |
| `FLAGFISH_LOG_FORMAT` / `FLAGFISH_LOG_LEVEL` | `json`/`info` in production |
| `FLAGFISH_IMAGE` / `FLAGFISH_VERSION` | image tag and stamped build version (default `flagfish:latest`, `dev`) |

These are the knobs the compose file passes through, not the whole process-level surface —
[environment.md](environment.md) is the exhaustive list and the source of truth for defaults.

Secrets live only in `deploy/.env` and are injected as environment variables. They are
never baked into an image layer (`.dockerignore` excludes `.env*` from the build context),
and the DSN password is redacted in logs. For a hardened deployment, inject them from your
orchestrator's secret store (Docker/Swarm secrets, k8s `Secret` + `envFrom`) instead of a
file on disk.

## First run: create the first admin

A freshly migrated instance is **not usable yet**. Until the `setup` config key says
otherwise, the route policy denies every route — `/login` and registration included — and
redirects to `/setup`. Only the anonymous branding endpoint (`GET /api/v1/instance`), the
theme and static assets, and the `/setup` page itself answer. That page is not a form; it
prints the command below and tells you to go run it.

The command is `flagfish admin create`. It writes the first admin, the `instance` singleton
row, and `config.setup` **in one transaction**, so a live instance always has an admin and
an instance with an admin is always live. The account is stamped `verified` — there is no
inbox behind a console command — so it can log in even with `verify_emails` on.

The rest of this document uses a shorthand for the compose invocation:

```sh
compose() { docker compose --env-file deploy/.env -f deploy/compose.yaml "$@"; }
```

Once `task deploy-up` reports healthy, run the bootstrap against the container:

```sh
compose exec flagfish /flagfish admin create --email you@example.com --name "Your Name"
```

- **`/flagfish` is spelled out** because `exec` does not go through the image's
  `ENTRYPOINT`, and a distroless image has no shell to resolve a bare name.
- **The password is prompted for**, with echo off, on the TTY that `exec` allocates. Prefer
  this: nothing lands in the process list or your shell history. The policy is the
  registration policy — 8 to 128 characters — and anything outside it is a hard error, not a
  warning.
- **`--mode users|teams` is fixed at setup** and immutable afterwards; it is the one setting
  you cannot change later. It defaults to `users`. Against an instance that is already set
  up, omitting it keeps whatever mode is in force, while passing one that disagrees is an
  error rather than a silently ignored flag. For a teams event, say so now:
  `--mode teams`.
- **`--name` defaults to the email** if you leave it out.
- **Re-running is safe.** A second account for an address that already exists fails loudly
  and points at `--promote`, which elevates an existing account instead of creating one (and
  is a no-op on an account that is already an admin).
- The insert is exempt from the `num_users` cap, so a full instance cannot lock its own
  first admin out.

**No restart is needed.** The bootstrap transaction ends with a `NOTIFY` on the channel every
running server listens to, so the process that was answering "setup incomplete" a moment ago
swaps in the new snapshot on commit.

For automation, pass the password through the environment instead of the prompt:

```sh
compose exec -e FLAGFISH_ADMIN_PASSWORD="$FLAGFISH_ADMIN_PASSWORD" flagfish \
  /flagfish admin create --email you@example.com --name "Your Name"
```

That value is visible in the *host's* process list for the life of the command, so on a
shared box prefer the prompt, and source it from your secret store rather than a file. If
the app container is not running — a crash loop, or a database you want to bootstrap before
serving — `compose run --rm flagfish admin create …` does the same thing in a throwaway
container (`run` goes through the entrypoint, so drop the `/flagfish`).

Now sign in at `https://your-host/login` and open `/admin`.

## Running the event

Everything from here is *instance* config: it lives in the database `config` table, is
edited in the admin console under `/admin/config` (or `PATCH /api/v1/admin/config`), and
takes effect across the fleet on commit — no restart, no redeploy. Times go over the API as
RFC 3339 and are stored as unix seconds.

**1. Name the event and set the clock.** `ctf_name`, `ctf_description`, and the three
timestamps `start`, `end`, `freeze`. The coherence rules are enforced at write and rejected
with a 422, not accepted and puzzled over later: `end` must be after `start`, and `freeze`
must fall between the two. Leave a timestamp unset and that boundary simply does not exist —
an event with no `start` is already running.

**2. Decide who can see what, before you open the doors.** `challenge_visibility` defaults
to `private` (authenticated callers only), `score_visibility` and `account_visibility`
default to `public`. `score_visibility` additionally accepts `hidden`, which means admins
only — the setting to reach for if you want no public board at all.

**3. Open registration.** `registration_visibility` is `public` by default, so registration
is open the moment setup completes. If you are pre-registering teams, set it to `private`
first and flip it when you are ready. In teams mode, `team_creation` gates whether players
may create teams at all, and `team_size` caps the members per team (`0` means unlimited, as
do `num_users` and `num_teams`).

**4. If you turn on `verify_emails`, configure mail first.** SMTP is *instance* config, not
environment — `mail_server`, `mail_port`, `mailfrom_addr`, and optionally
`mail_username`/`mail_password` — and a `mail_server` without a valid port and from-address
is rejected. Turning verification on with no mailer configured leaves every player able to
register and log in but locked out of challenges, submissions and hint unlocks, with no way
to confirm. Admins are exempt from the verification gate, so you will not notice from your
own account.

**5. Load the challenges** through the admin console, or import an existing archive — the
console's `/admin` page and `flagfish import` both accept a CTFd archive, one-way. This is
the only foreign format the code speaks; nothing goes back out in it.

**6. Run the event.** The admin shell carries a one-click **Pause** switch on every admin
page. Pausing refuses every flag submission with a 403 — for players *and* admins, with no
exemption — and shows players a "submissions are paused" banner with the flag input
disabled. It gates exactly one thing: the attempt endpoint. Browsing, scoreboards and hint
unlocks keep working while paused, so a paused event is still one where score can move.

**7. Freeze.** The `freeze` timestamp does this on its own — there is no separate boolean to
flip. Setting `view_after_ctf` lets players keep browsing challenges after `end`.

**8. Export the final standings.** The admin console's **Export** card on `/admin` links to
three CSVs, also reachable directly as an authenticated admin:

```
GET /api/v1/admin/export/standings.csv    rank, account_id, name, bracket, score, last_event, hidden, banned
GET /api/v1/admin/export/users.csv
GET /api/v1/admin/export/teams.csv        (teams mode)
```

All three are admin-only and take no parameters. The standings export is the **admin** board,
not the public one: hidden and banned rows are present and flagged rather than dropped, so
what you hand the prize committee shows who was excluded and why they are missing from the
public scoreboard.

## Backup and restore

`flagfish` writes its own archive format in two profiles, and the difference is the whole
point:

- **full** (`--backup`) — verbatim. Password hashes, flag content, hint content, submission
  bodies and IPs, the tracking and audit tables, secret config values. This is the only
  archive `restore` accepts. **Treat the file as a credential.**
- **shareable** (`--safe`) — the same instance, field-masked: password hashes, flag and hint
  contents, submission bodies and IPs are dropped, the `tracking` and `audit_log` tables are
  omitted entirely, and every secret config value is nulled. Restoring one is refused with
  an error that tells you to re-export with `--backup`. Note that account **email addresses
  are not masked** — a "shareable" archive is still personal data.

The two entry points default differently, deliberately: the CLI `flagfish export` defaults to
`--safe` (you asked for a file to hand out), while the console's **Backup & restore** card
and `POST /api/v1/admin/backup` default to the full profile (you asked for a backup).

**Quiesce first.** Take backups from a paused instance: hit Pause in the admin console,
confirm the pause banner, then start the backup. An export is a consistent read, but a
backup taken mid-event is a backup that is missing solves the moment it finishes, and
restoring it silently rolls those players back. Pause does not stop *everything* — hint
unlocks still charge score — so for a true point-in-time artifact, take the backup after
`end` or during a maintenance window.

### From the admin console

`/admin` → **Backup & restore** → *Back up (full)* or *Back up (shareable)*. The work runs
as an async task on the job queue with a live progress bar; when it finishes, a **Download
backup** link appears. The same card takes an upload for *Restore a backup…* and *Import a
CTFd archive…*. One operation of each kind runs at a time — a second request gets a 409 and
says so. This is the path that needs no shell on the box.

### From the CLI

Mount a directory the container can write to, and run the export in a throwaway container:

```sh
mkdir -p backups && chmod 777 backups     # the image runs as nonroot (uid 65532)
compose run --rm -v "$PWD/backups:/backups" flagfish export --backup /backups/flagfish.zip
compose run --rm -v "$PWD/backups:/backups" flagfish restore /backups/flagfish.zip
```

**Restore replaces the instance.** It is not a merge and not an import into an empty
database: it truncates every table it owns and reinstates the archive's rows in one
transaction, so a failure rolls back cleanly but a success overwrites whatever was there.
It also refuses an archive taken at a different schema version — migrate to the archive's
version first. Blob content is written to object storage *before* the transaction opens, so
a rolled-back restore can leave orphaned objects behind; they are harmless and collectable.

### Back up the database too

The archive format is a migration and disaster-recovery tool for *this* application. It is
not a substitute for a database backup, and it does not capture anything the app does not
model.

```sh
compose exec -T postgres pg_dump -U flagfish -d flagfish -Fc > flagfish-$(date +%F).dump
```

Adjust the role and database if you changed `POSTGRES_USER` / `POSTGRES_DB`. `pg_dump` does
not cover challenge files — those are objects in MinIO, so back up the `miniodata` volume
alongside it, or point the app at object storage that is backed up for you. Add WAL
archiving if you are running an event that matters, and test the restore before you need it.
Production should use managed Postgres or a backed-up volume rather than the bundled
service.

## TLS and production notes

The app serves plaintext HTTP on `:8000`. Terminate TLS at a reverse proxy (Caddy, nginx,
Traefik, or a load balancer) in front of it, and set `FLAGFISH_TRUSTED_PROXIES` to the
proxy's address — otherwise every request, and every anti-cheat IP, is attributed to the
proxy, and the anonymous rate limiter sees the whole internet as one caller. Session cookies
are marked `Secure` by default, which is what an `https://` deployment wants; if you are
deliberately serving plain HTTP, `FLAGFISH_SECURE_COOKIES=false` is the switch, and the
cookie then travels in the clear.

Require TLS to the database (`sslmode=require`) if you point the app at a managed Postgres
instead of the bundled one. Set memory limits per service (the compose file ships
conservative defaults under `deploy.resources.limits`). For backups, see above — the
`pgdata` and `miniodata` volumes are the state that matters.
