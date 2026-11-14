#!/usr/bin/env bash
set -uo pipefail
cd "$(dirname "$0")"

# On native Linux use host networking, which has no port limit. On Docker
# Desktop (Windows/macOS) and WSL host networking is not reliable, so the
# compose file that publishes a port range is used there.
COMPOSE=app/docker-compose.yml
if [ "$(uname -s)" = "Linux" ] && ! grep -qiE "microsoft|wsl" /proc/version 2>/dev/null; then
    COMPOSE=app/docker-compose.linux.yml
fi

mkdir -p app/logs
LOG=app/logs/install.log
: > "$LOG"

echo "=============================================================="
echo "  Facial Device Emulator - Install (Docker)"
echo "=============================================================="

echo "[1/3] Checking Docker ..."
if ! docker info >>"$LOG" 2>&1; then
    echo
    echo "[ERROR] Docker is not running or your user has no permission to use it."
    echo "        Start Docker and run ./install.sh again."
    exit 1
fi
echo "      Docker OK."

echo "[2/3] Loading the application image ..."
if ! docker load -i app/facial-device-emulator-image.tar >>"$LOG" 2>&1; then
    echo
    echo "[ERROR] Could not load the image. See app/logs/install.log"
    exit 1
fi
echo "      Image loaded."

echo "[3/3] Preparing the database ..."
if ! docker compose -f "$COMPOSE" up -d postgres >>"$LOG" 2>&1; then
    echo
    echo "[ERROR] Could not prepare the database. See app/logs/install.log"
    exit 1
fi

# "docker compose up -d" only confirms the container started, not that the
# database accepts connections. --wait would solve it but needs Docker
# Compose 2.17+; the package supports 2.0, so poll the healthcheck instead.
db_ready=0
for _ in $(seq 1 120); do
    status=$(docker inspect -f '{{.State.Health.Status}}' facial-device-emulator-db 2>/dev/null || true)
    if [ "$status" = "healthy" ]; then
        db_ready=1
        break
    fi
    sleep 1
done
if [ "$db_ready" -ne 1 ]; then
    echo
    echo "[ERROR] The database did not become ready in time. See app/logs/install.log"
    exit 1
fi
echo "      Database ready."

echo
echo "[OK] Installed. Run ./start.sh"
