# Environment & configuration

Scope: the process-level environment for the `flagfish` static binary, in development and in
production. For the docker compose stack, see [deploy.md](deploy.md).

## Source of truth

`internal/config/env.go` (`Env` + `LoadEnv`) is authoritative for every process-level knob, and the
table below tracks it. The repo-root `.env.example` lists the same set for a local checkout.

Under docker compose you edit `deploy/.env` instead (`deploy/.env.example` is its template). That
file carries only the subset `deploy/compose.yaml` passes through to the container, not the whole
surface below — see [deploy.md](deploy.md#environment).

Runtime *instance* config — event name, start/end/freeze times, visibility — is not here. It lives
in the database `config` table and is edited through the admin API.

```sh
flagfish env      # print the resolved config (password redacted); exits non-zero on any problem
```

Run it **before** a deploy, not after. It fails loudly and lists every problem at once.

## Server variables

| Variable | Default | Meaning |
| --- | --- | --- |
| `FLAGFISH_DATABASE_URL` (alias `DATABASE_URL`) | — (**required**) | Postgres 17 DSN; the scheme must be `postgres`/`postgresql` and must name a database |
| `FLAGFISH_ADDR` | `:8000` | Listen address — `host:port`, or `:port` to bind all interfaces |
| `FLAGFISH_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `FLAGFISH_LOG_FORMAT` | `json` | `text` \| `json`. Use `json` in production. |
| `FLAGFISH_RATE_LIMIT` | `60` | Requests per window, per caller, before the limiter denies. Positive integer. |
| `FLAGFISH_AUTH_RATE_LIMIT` | `10` | Failed credential attempts per window against ONE target — the account named by the submitted email, or the submitted token. Positive integer. |
| `FLAGFISH_AUTH_IP_FAILURE_LIMIT` | `60` | Failed credential attempts per window from one source address. A successful attempt is refunded. Positive integer, and must not exceed `FLAGFISH_AUTH_IP_RATE_LIMIT`. |
| `FLAGFISH_AUTH_IP_RATE_LIMIT` | `300` | All credential attempts per window from one source address, successful or not. Positive integer. |
| `FLAGFISH_RATE_WINDOW` | `1m` | Window over which both rate limits are counted. Go duration, must be positive. |
| `FLAGFISH_TRUSTED_PROXIES` | empty | Comma-separated CIDRs (or bare IPs) whose `X-Forwarded-For` is believed. Empty = trust nobody, use the socket peer address. |
| `FLAGFISH_SECURE_COOKIES` | `true` | Mark session cookies `Secure`. Defaults on; `false` only when you serve plain HTTP on purpose. |
| `FLAGFISH_MAX_UPLOAD_BYTES` | `33554432` | Largest multipart upload accepted (32 MiB). Every other body is capped at 1 MiB. Positive integer. |
| `FLAGFISH_DB_MAX_CONNS` | `25` | pgx pool ceiling. At least 1. |
| `FLAGFISH_DB_MIN_CONNS` | `2` | pgx pool floor. A minimum above the maximum is a boot error. |

**Shared addresses are not penalised.** A whole university or a venue's wifi behind one outbound
address is one source, so the strict budget is keyed on the account being guessed at rather than on
where the guess came from: distinct players never contend, however many share an address. The source
address keeps a strict budget on failures — refunded on success, so players logging in normally
never spend it — and a generous one on everything, which bounds the password verifications a
stranger can ask for. Raise `FLAGFISH_AUTH_IP_RATE_LIMIT` only if a single address legitimately
carries hundreds of sign-ins a minute; lowering `FLAGFISH_AUTH_RATE_LIMIT` tightens brute-force
protection without affecting shared addresses at all.

**`FLAGFISH_TRUSTED_PROXIES` deserves a second look.** A wrong value here breaks nothing visibly —
it just attributes every request to the proxy's own address, which quietly poisons the anti-cheat
evidence in `submissions.ip`. Behind a reverse proxy, set it to the proxy's address(es). A
malformed entry is a startup error rather than a skipped one, deliberately.

## Object storage (challenge files)

Challenge files live in S3-compatible object storage (AWS S3, MinIO, Cloudflare R2, …). Credentials
come from the environment **only** — never a file, never the config table.

| Variable | Default | Meaning |
| --- | --- | --- |
| `FLAGFISH_S3_ENDPOINT` | — | `host:port`, no scheme |
| `FLAGFISH_S3_BUCKET` | — | Bucket the app stores challenge files in |
| `FLAGFISH_S3_REGION` | `us-east-1` | |
| `FLAGFISH_S3_ACCESS_KEY` | — | |
| `FLAGFISH_S3_SECRET_KEY` | — | |
| `FLAGFISH_S3_USE_SSL` | `true` | `false` for a local MinIO over plain HTTP |
| `FLAGFISH_S3_PATH_STYLE` | `false` | `true` for MinIO and most self-hosted backends |

Leave these unset to run without file storage: upload and download are disabled and say so. Setting
*some but not all* of them is a boot error that names what is missing — a half-configured object
store is not something we let you deploy.

## First-admin bootstrap

A freshly migrated instance has no admin, and `role='admin'` is only grantable through the admin
API — which already requires an admin. `flagfish admin create` breaks that deadlock from the
console. It reads `DATABASE_URL` like every database command, so a missing DSN is a hard error.

```sh
# Preferred: password from the environment, never on the argv (where `ps` and shell history
# would expose it). Creates a verified admin.
FLAGFISH_ADMIN_PASSWORD='…' flagfish admin create --email you@example.com --name You

# Elevate an account that already registered through the UI instead of creating a new one:
flagfish admin create --email you@example.com --promote
```

Under docker compose this is the whole of first-run setup, and
[deploy.md](deploy.md#first-run-create-the-first-admin) is the canonical walkthrough — it has the
exact `docker compose exec` invocation and what to configure next. Do not bootstrap by hand with
`psql`: the two rows that make an instance live belong in the same transaction as the admin.

- Password source order: `--password` (discouraged), then `FLAGFISH_ADMIN_PASSWORD`, then an
  interactive no-echo prompt if stdin is a TTY. Policy matches registration: 8–128 characters.
  Empty or weak passwords, and a missing DSN, are hard errors.
- `--mode users|teams` fixes the account model in the same transaction, and it is immutable
  afterwards. Defaulted, it yields to whatever an already-set-up instance plays; typed and
  disagreeing, it is an error rather than a flag that was quietly ignored.
- The account is stamped `verified`: there is no inbox flow behind a console command, so the first
  admin can sign in even with `verify_emails` on.
- The transaction ends with a `NOTIFY`, so a server that is already running picks the change up on
  commit instead of needing a restart.
- The insert bypasses the `num_users` cap (the caps trigger is suppressed for that one transaction,
  the same exemption the importer runs under), so a full instance cannot lock its own first admin
  out.
- Idempotent where it matters: creating a second account for an existing email fails loudly and
  points at `--promote`; `--promote` on an account that is already an admin is a no-op, not an
  error.

## Production hardening

**Secrets.** The DSN carries the database password, and in development it comes from an environment
variable or a `.env` file. In production, inject secrets from your orchestrator's secret store —
Docker/Swarm secrets, a Kubernetes `Secret` plus `envFrom`, or systemd `LoadCredential` — rather
than a `.env` on disk. Two invariants hold today and must keep holding: `.dockerignore` excludes
`.env*` from the build context, and the DSN password is redacted in logs. Never bake a secret into
an image layer.

**TLS.** The binary serves plaintext HTTP on `:8000` and does no TLS termination. Terminate TLS at
a reverse proxy (Caddy, nginx, Traefik) or a load balancer in front of it, and set
`FLAGFISH_TRUSTED_PROXIES` to that proxy's address. Require TLS on the database connection too —
`sslmode=require` or `verify-full`, never `disable` outside development.

**Migrations on deploy.** `flagfish migrate` and `serve --migrate` both take a `pg_advisory_lock`,
so both are safe with N replicas. Recommended: run `flagfish migrate` as a pre-deploy step or an
init container and gate the rollout on its success, so a failed migration blocks the deploy instead
of letting a half-migrated app take traffic. Keep `serve --migrate` for the single-container
self-hoster, where it is a genuine convenience.

**Backup and restore.** Schedule `pg_dump` (custom format), and add WAL archiving for
point-in-time recovery if you are running an event that matters. Test the restore before you need
it. Production should use managed Postgres or a backed-up volume — never the compose development
service. `flagfish export --backup <out.zip>` and `flagfish restore <in.zip>` round-trip an instance
in flagfish's own format; that is a migration and disaster-recovery tool, not a substitute for
database backups. Restore *replaces* an instance rather than merging into one, and the archive
profiles differ in what they carry — see [deploy.md](deploy.md#backup-and-restore) before you rely
on either.

**Resource limits.** Set CPU and memory limits and requests on the app. Size Postgres
`max_connections` against `FLAGFISH_DB_MAX_CONNS` times the number of app replicas, and leave
headroom: the config listener parks on one pool connection for the life of the process, blocked in
`LISTEN`, so the pool serves queries with one fewer than its ceiling.

**Observability.** The binary exports Prometheus metrics on `GET /metrics` and a readiness check
that actually pings the pool on `GET /readyz`. Both mount outside the authenticated chain — they
are infrastructure, not a logged-in caller — so keep them off the public internet at your proxy.
Keep structured JSON logs on (`FLAGFISH_LOG_FORMAT=json`, the default). Add a liveness probe
against `GET /healthz` at the orchestrator layer: the distroless image ships no shell and no curl,
so the binary probes itself with `flagfish healthcheck`.

There is **no tracing exporter**. The binary has no OpenTelemetry dependency and reads no `OTEL_*`
variable, so setting `OTEL_EXPORTER_OTLP_ENDPOINT` does nothing at all. Traces are a roadmap item;
until they land, metrics and logs are the whole story.
