# Environment & configuration

Scope: the process-level environment for the `flagfish` static binary, in development and in
production. For the docker compose stack, see [deploy.md](deploy.md).

## Source of truth

`internal/config/env.go` (`Env` + `LoadEnv`) is authoritative for every process-level knob.
`.env.example` mirrors it exactly and is the one file a self-hoster reads.

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
| `FLAGFISH_RATE_WINDOW` | `1m` | Window over which the rate limit is counted. Go duration, must be positive. |
| `FLAGFISH_TRUSTED_PROXIES` | empty | Comma-separated CIDRs (or bare IPs) whose `X-Forwarded-For` is believed. Empty = trust nobody, use the socket peer address. |

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

Under docker compose, run it as a one-shot against the app image once the database is migrated:

```sh
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm \
  -e FLAGFISH_ADMIN_PASSWORD \
  flagfish admin create --email you@example.com --name You
```

- Password source order: `--password` (discouraged), then `FLAGFISH_ADMIN_PASSWORD`, then an
  interactive no-echo prompt if stdin is a TTY. Policy matches registration: 8–128 characters.
  Empty or weak passwords, and a missing DSN, are hard errors.
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
database backups.

**Resource limits.** Set CPU and memory limits and requests on the app. Size Postgres
`max_connections` against the app's pgx pool configuration.

**Observability.** Set `OTEL_EXPORTER_OTLP_ENDPOINT` to your collector to export traces. Keep
structured JSON logs on (`FLAGFISH_LOG_FORMAT=json`, the default). Add a liveness/readiness probe
against `GET /healthz` at the orchestrator layer: the distroless image ships no shell and no curl,
so the binary probes itself with `flagfish healthcheck`.
