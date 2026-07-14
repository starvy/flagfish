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
| `FLAGFISH_LOG_FORMAT` / `FLAGFISH_LOG_LEVEL` | `json`/`info` in production |

Secrets live only in `deploy/.env` and are injected as environment variables. They are
never baked into an image layer (`.dockerignore` excludes `.env*` from the build context),
and the DSN password is redacted in logs. For a hardened deployment, inject them from your
orchestrator's secret store (Docker/Swarm secrets, k8s `Secret` + `envFrom`) instead of a
file on disk.

## First-run setup (required)

A freshly migrated instance is **not usable until it is marked set up and an admin
exists**. There is no self-serve setup page and no admin-bootstrap endpoint by design —
until then every route except `/api/v1/instance` answers "setup incomplete". Complete it
once, out of band:

```sh
# 1. seed the singleton instance row and mark setup done, then wake the running
#    server's config watcher so the change takes effect without a restart.
docker compose --env-file deploy/.env -f deploy/compose.yaml exec -T postgres \
  psql -U flagfish -d flagfish <<'SQL'
INSERT INTO instance (user_mode, version) VALUES ('users', 'manual')
  ON CONFLICT (id) DO NOTHING;
INSERT INTO config (key, value) VALUES ('setup', 'true')
  ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
SELECT pg_notify('config_changed', '');
SQL

# 2. register your admin account through the UI/API (POST /api/v1/register), then
#    promote it. The role is read fresh on every request, so no restart is needed.
docker compose --env-file deploy/.env -f deploy/compose.yaml exec -T postgres \
  psql -U flagfish -d flagfish -c \
  "UPDATE users SET role = 'admin' WHERE email = 'you@example.com';"
```

For a **teams-mode** event, seed `instance.user_mode` and the `user_mode` config key as
`'teams'` in step 1 (before the first team is created); the mode is fixed at setup and
immutable thereafter. Everything else — event name, start/end/freeze, visibility — is then
editable through the admin API (`PATCH /api/v1/admin/config`).

## TLS and production notes

The app serves plaintext HTTP on `:8000`. Terminate TLS at a reverse proxy (Caddy, nginx,
Traefik, or a load balancer) in front of it, and set `FLAGFISH_TRUSTED_PROXIES` to the
proxy's address — otherwise every request, and every anti-cheat IP, is attributed to the
proxy. Require TLS to the database (`sslmode=require`) if you point the app at a managed
Postgres instead of the bundled one. Set memory limits per service (the compose file ships
conservative defaults under `deploy.resources.limits`) and back up the `pgdata` volume.
