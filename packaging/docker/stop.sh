#!/usr/bin/env bash
set -uo pipefail
cd "$(dirname "$0")"

# Same compose selection as install.sh.
COMPOSE=app/docker-compose.yml
if [ "$(uname -s)" = "Linux" ] && ! grep -qiE "microsoft|wsl" /proc/version 2>/dev/null; then
    COMPOSE=app/docker-compose.linux.yml
fi

mkdir -p app/logs
LOG=app/logs/install.log

echo "Stopping the Facial Device Emulator ..."
if ! docker compose -f "$COMPOSE" stop >>"$LOG" 2>&1; then
    echo
    echo "[ERROR] Could not stop the services. See app/logs/install.log"
    exit 1
fi
echo
echo "[OK] Stopped. Data is kept."
