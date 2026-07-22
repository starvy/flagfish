#!/usr/bin/env bash
#
# Boot both real, isolated flagfish instances for the Playwright suite, idempotently.
#
# It builds the SPA (the binary serves it from go:embed, so it MUST exist first), builds the binary
# once, then for each account model brings up throwaway Postgres + MinIO, resets the database to a
# clean migrated state, creates the first admin and starts the server with its worker. Re-running it
# converges on the same clean slate — so the suite is reproducible, not a one-off.
#
#   web/e2e/scripts/up.sh && (cd web && npm run e2e)
#
# Set FLAGFISH_E2E_SKIP_BUILD=1 to reuse an already-built SPA and binary while iterating on specs.
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/env.sh"

say() { printf '  \033[36m›\033[0m %s\n' "$*"; }
die() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

command -v docker >/dev/null || die "docker is required"
command -v go >/dev/null || die "go is required"

# ---------------------------------------------------------------- build the SPA + binary (once)
if [ "${FLAGFISH_E2E_SKIP_BUILD:-0}" != "1" ]; then
  say "building the SPA (go:embed serves it, so it must exist before the binary)"
  ( cd "${FF_REPO_ROOT}/web" && npm install --no-audit --no-fund >/dev/null && npm run build >/dev/null )
  rm -rf "${FF_REPO_ROOT}/internal/web/dist/assets" "${FF_REPO_ROOT}/internal/web/dist/index.html"
  cp -R "${FF_REPO_ROOT}/web/dist/." "${FF_REPO_ROOT}/internal/web/dist/"
  say "building the flagfish binary"
  ( cd "${FF_REPO_ROOT}" && CGO_ENABLED=0 go build -o bin/flagfish ./cmd/flagfish )
fi
BIN="${FF_REPO_ROOT}/bin/flagfish"
[ -x "${BIN}" ] || die "no binary at ${BIN} (run without FLAGFISH_E2E_SKIP_BUILD)"

ensure_container() {
  local name="$1"; shift
  if [ -n "$(docker ps -aq -f "name=^${name}$")" ]; then
    if [ -z "$(docker ps -q -f "name=^${name}$")" ]; then
      say "starting existing container ${name}"; docker start "${name}" >/dev/null
    fi
  else
    say "creating container ${name}"; docker run -d --name "${name}" "$@" >/dev/null
  fi
}

boot() {
  local mode="$1"; read -r pg_port minio_port app_port <<<"$(ff_ports "${mode}")"
  local pg="ff-asm-${mode}-pg" minio="ff-asm-${mode}-minio"
  local url="http://localhost:${app_port}"

  say "[${mode}] containers (pg ${pg_port}, minio ${minio_port})"
  ensure_container "${pg}" -p "${pg_port}:5432" \
    -e POSTGRES_USER=flagfish -e POSTGRES_PASSWORD=flagfish -e POSTGRES_DB=flagfish "${FF_PG_IMAGE}"
  ensure_container "${minio}" -p "${minio_port}:9000" \
    -e "MINIO_ROOT_USER=${FF_S3_ACCESS_KEY}" -e "MINIO_ROOT_PASSWORD=${FF_S3_SECRET_KEY}" \
    "${FF_MINIO_IMAGE}" server /data

  say "[${mode}] waiting for Postgres"
  for i in $(seq 1 60); do docker exec "${pg}" pg_isready -U flagfish >/dev/null 2>&1 && break; [ "$i" = 60 ] && die "[${mode}] Postgres not ready"; sleep 1; done
  say "[${mode}] waiting for MinIO"
  for i in $(seq 1 60); do curl -fsS "http://localhost:${minio_port}/minio/health/live" >/dev/null 2>&1 && break; [ "$i" = 60 ] && die "[${mode}] MinIO not ready"; sleep 1; done

  # A fresh, EMPTY bucket every boot: backups and uploaded archives from a prior run would otherwise
  # linger and a restore could revert the instance to stale bytes.
  say "[${mode}] resetting bucket ${FF_S3_BUCKET}"
  docker run --rm --network host --entrypoint sh "${FF_MC_IMAGE}" -c "
    mc alias set ff 'http://localhost:${minio_port}' '${FF_S3_ACCESS_KEY}' '${FF_S3_SECRET_KEY}' >/dev/null &&
    mc rb --force ff/${FF_S3_BUCKET} >/dev/null 2>&1 || true &&
    mc mb ff/${FF_S3_BUCKET} >/dev/null" || die "[${mode}] bucket reset failed"

  say "[${mode}] resetting database"
  docker exec -e PGPASSWORD=flagfish "${pg}" psql -U flagfish -d postgres -v ON_ERROR_STOP=1 -q \
    -c "DROP DATABASE IF EXISTS flagfish WITH (FORCE);" -c "CREATE DATABASE flagfish;" >/dev/null \
    || die "[${mode}] database reset failed"

  # The server environment for this instance.
  export FLAGFISH_DATABASE_URL="postgres://flagfish:flagfish@localhost:${pg_port}/flagfish?sslmode=disable"
  export FLAGFISH_S3_ENDPOINT="localhost:${minio_port}"
  export FLAGFISH_S3_BUCKET="${FF_S3_BUCKET}"
  export FLAGFISH_S3_ACCESS_KEY="${FF_S3_ACCESS_KEY}"
  export FLAGFISH_S3_SECRET_KEY="${FF_S3_SECRET_KEY}"
  export FLAGFISH_S3_PATH_STYLE="true"
  export FLAGFISH_S3_USE_SSL="false"
  export FLAGFISH_ADDR=":${app_port}"
  export FLAGFISH_SECURE_COOKIES="false"
  # A login-heavy suite from one IP would trip the CREDENTIAL rate limiter; lift those for the TEST
  # instance only. failure must not exceed rate, so they are equal. The general per-account/route
  # limiter keeps its default — a gameplay spec asserts that rapid submits trip it.
  export FLAGFISH_AUTH_RATE_LIMIT="1000000"
  export FLAGFISH_AUTH_IP_RATE_LIMIT="1000000"
  export FLAGFISH_AUTH_IP_FAILURE_LIMIT="1000000"

  say "[${mode}] applying migrations"
  "${BIN}" migrate >/dev/null 2>&1 || die "[${mode}] migrate failed"

  say "[${mode}] creating the first admin (${FF_ADMIN_EMAIL}, ${mode} mode)"
  FLAGFISH_ADMIN_PASSWORD="${FF_ADMIN_PASSWORD}" \
    "${BIN}" admin create --email "${FF_ADMIN_EMAIL}" --name "E2E Admin" --mode "${mode}" >/dev/null \
    || die "[${mode}] admin create failed"

  local log="${FF_SCRIPTS_DIR}/.server-${mode}.log" pidf="${FF_SCRIPTS_DIR}/.server-${mode}.pid"
  if [ -f "${pidf}" ] && kill -0 "$(cat "${pidf}")" 2>/dev/null; then
    kill "$(cat "${pidf}")" 2>/dev/null || true
    for _ in $(seq 1 20); do kill -0 "$(cat "${pidf}")" 2>/dev/null || break; sleep 0.2; done
  fi

  say "[${mode}] starting server (serve --with-worker) on :${app_port}"
  nohup "${BIN}" serve --with-worker >"${log}" 2>&1 &
  echo $! > "${pidf}"
  for i in $(seq 1 60); do
    [ "$(curl -s -o /dev/null -w '%{http_code}' "${url}/healthz" 2>/dev/null || true)" = "200" ] && break
    kill -0 "$(cat "${pidf}")" 2>/dev/null || { tail -n 20 "${log}" >&2 || true; die "[${mode}] server exited during startup"; }
    [ "$i" = 60 ] && die "[${mode}] server did not become healthy (see ${log})"
    sleep 1
  done
  say "[${mode}] ready — ${url}"
}

# Stop any prior servers before rebooting.
for m in "${FF_MODES[@]}"; do
  pidf="${FF_SCRIPTS_DIR}/.server-${m}.pid"
  [ -f "${pidf}" ] && kill "$(cat "${pidf}")" 2>/dev/null || true
done
pkill -f "${BIN} serve" 2>/dev/null || true

for m in "${FF_MODES[@]}"; do boot "${m}"; done

printf '\n  \033[32m✓ both stacks ready\033[0m — teams %s, users %s\n' "${FLAGFISH_TEAMS_URL}" "${FLAGFISH_USERS_URL}"
say "run the suite: cd web && npm run e2e"
