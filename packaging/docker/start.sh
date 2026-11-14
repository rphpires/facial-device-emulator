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

echo "=============================================================="
echo "  Facial Device Emulator - Start"
echo "=============================================================="

if ! docker info >/dev/null 2>&1; then
    echo "[ERROR] Docker is not running. Start Docker and try again."
    exit 1
fi

if ! docker image inspect facial-device-emulator:1.0 >/dev/null 2>&1; then
    echo "[ERROR] The application is not installed yet. Run ./install.sh first."
    exit 1
fi

echo "[1/2] Starting the services ..."
if ! docker compose -f "$COMPOSE" up -d >>"$LOG" 2>&1; then
    echo "[ERROR] Could not start the services. See app/logs/install.log"
    exit 1
fi

echo "[2/2] Waiting for the application to answer ..."
for _ in $(seq 1 60); do
    if curl -sf http://localhost:7070/monitoring/health/quick >/dev/null 2>&1; then
        echo
        echo "[OK] Running at http://localhost:7070"
        echo
        echo "     To stop: ./stop.sh"
        exit 0
    fi
    sleep 1
done

echo
echo "[ERROR] The application did not answer within 60 seconds."
echo "        See the log with: docker compose -f $COMPOSE logs app"
exit 1
