#!/usr/bin/env bash
# Shared configuration for the Playwright e2e stacks. Sourced by up.sh and down.sh, and handy to
# `source` by hand to get the same values the suite defaults to.
#
# Two throwaway instances run side by side, on deliberately non-default ports so they never collide
# with the dev stack in compose.yaml:
#   teams  — Postgres 5588, MinIO 9370, app http://localhost:8019   (account model: teams)
#   users  — Postgres 5589, MinIO 9371, app http://localhost:8020   (account model: users)

FF_PG_IMAGE="postgres:17-alpine"
FF_MINIO_IMAGE="minio/minio:RELEASE.2025-04-08T15-41-24Z"
FF_MC_IMAGE="minio/mc:RELEASE.2025-04-08T15-39-49Z"

FF_S3_ACCESS_KEY="flagfishtest"
FF_S3_SECRET_KEY="flagfishtest123"
FF_S3_BUCKET="flagfish"

FF_ADMIN_EMAIL="admin@example.com"
FF_ADMIN_PASSWORD="AdminPass123!"

# mode → "pg_port minio_port app_port". The container names derive from the mode.
FF_MODES=(teams users)
ff_ports() { case "$1" in teams) echo "5588 9370 8019";; users) echo "5589 9371 8020";; esac; }

FF_SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FF_REPO_ROOT="$(cd "${FF_SCRIPTS_DIR}/../../.." && pwd)"

# What the suite reads (its config defaults match these, so nothing needs exporting for a local run).
export FLAGFISH_TEAMS_URL="http://localhost:8019"
export FLAGFISH_USERS_URL="http://localhost:8020"
export FLAGFISH_E2E_ADMIN_EMAIL="${FF_ADMIN_EMAIL}"
export FLAGFISH_E2E_ADMIN_PASSWORD="${FF_ADMIN_PASSWORD}"
export FLAGFISH_E2E_DATABASE_URL="postgres://flagfish:flagfish@localhost:5588/flagfish?sslmode=disable"
