#!/usr/bin/env bash
#
# Tear down everything up.sh started: both server processes and all four throwaway containers.
set -uo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/env.sh"

say() { printf '  \033[36m›\033[0m %s\n' "$*"; }

for m in "${FF_MODES[@]}"; do
  pidf="${FF_SCRIPTS_DIR}/.server-${m}.pid"
  if [ -f "${pidf}" ]; then
    PID="$(cat "${pidf}")"
    if kill -0 "${PID}" 2>/dev/null; then say "stopping ${m} server (pid ${PID})"; kill "${PID}" 2>/dev/null || true; fi
    rm -f "${pidf}"
  fi
done
pkill -f "${FF_REPO_ROOT}/bin/flagfish serve" 2>/dev/null || true

say "removing containers"
docker rm -f ff-asm-teams-pg ff-asm-teams-minio ff-asm-users-pg ff-asm-users-minio >/dev/null 2>&1 || true

printf '  \033[32m✓ down\033[0m\n'
