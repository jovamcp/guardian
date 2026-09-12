#!/usr/bin/env bash
# Envoltorio fino: `guardianctl agent run` es la implementación real (Go, stdlib).
# Se mantiene para poder lanzar un agente desde cron/systemd sin recordar la ruta del binario.
#   sudo sandbox/runner.sh <nombre-o-manifiesto> [--dry-run]
set -euo pipefail
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${REPO_DIR}/bin/guardianctl"
[[ -x "${BIN}" ]] || (cd "${REPO_DIR}" && go build -o bin/guardianctl ./cmd/guardianctl)
exec "${BIN}" agent run "$@"
