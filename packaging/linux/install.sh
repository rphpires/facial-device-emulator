#!/usr/bin/env bash
set -uo pipefail
cd "$(dirname "$0")"

mkdir -p app/logs
LOG=app/logs/install.log
: > "$LOG"

echo "=============================================================="
echo "  Facial Device Emulator - Install (Linux)"
echo "=============================================================="

if [ "$(id -u)" -ne 0 ]; then
    echo
    echo "[ERROR] Run with sudo:  sudo ./install.sh"
    exit 1
fi

if grep -qiE "microsoft|wsl" /proc/version 2>/dev/null; then
    ON_WSL=1
    echo "      Environment detected: WSL"
else
    ON_WSL=0
    echo "      Environment detected: Linux"
fi

echo
echo "Which port range will the emulated devices use?"
echo "Use the range configured on the devices."
printf "Range [4000-4499]: "
read -r RANGE
RANGE="${RANGE:-4000-4499}"

case "$RANGE" in
    *-*) PORT_START="${RANGE%%-*}"; PORT_END="${RANGE##*-}" ;;
    *)   PORT_START="$RANGE"; PORT_END="$RANGE" ;;
esac

if ! echo "$PORT_START" | grep -qE '^[0-9]+$' \
   || ! echo "$PORT_END" | grep -qE '^[0-9]+$' \
   || [ "$PORT_START" -gt "$PORT_END" ]; then
    echo
    echo "[ERROR] Invalid range: $RANGE. Use the format 4000-4499."
    exit 1
fi

PORT_COUNT=$((PORT_END - PORT_START + 1))

echo "[1/5] Installing PostgreSQL ..."
if ! command -v psql >/dev/null 2>&1; then
    apt-get update >>"$LOG" 2>&1
    if ! apt-get install -y postgresql postgresql-contrib >>"$LOG" 2>&1; then
        echo
        echo "[ERROR] Could not install PostgreSQL. See app/logs/install.log"
        exit 1
    fi
fi
echo "      PostgreSQL OK."

echo "[2/5] Starting the database ..."
if [ "$ON_WSL" -eq 1 ]; then
    service postgresql start >>"$LOG" 2>&1
else
    systemctl enable --now postgresql >>"$LOG" 2>&1
fi

for _ in $(seq 1 30); do
    if su - postgres -c "pg_isready" >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

if ! su - postgres -c "pg_isready" >/dev/null 2>&1; then
    echo
    echo "[ERROR] The database did not become ready. See app/logs/install.log"
    exit 1
fi
echo "      Database running."

echo "[3/5] Creating the application user and database ..."
su - postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='emulator'\"" 2>>"$LOG" | grep -q 1 \
  || su - postgres -c "psql -c \"CREATE USER emulator WITH PASSWORD 'emulator123';\"" >>"$LOG" 2>&1

su - postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='emulator_db'\"" 2>>"$LOG" | grep -q 1 \
  || su - postgres -c "psql -c \"CREATE DATABASE emulator_db OWNER emulator;\"" >>"$LOG" 2>&1

if ! su - postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='emulator_db'\"" 2>>"$LOG" | grep -q 1; then
    echo
    echo "[ERROR] Could not create the emulator_db database. See app/logs/install.log"
    exit 1
fi
echo "      User and database OK."

if ! chmod +x app/facial-device-emulator start.sh stop.sh; then
    echo
    echo "[ERROR] Could not set the execute permission on the scripts."
    exit 1
fi

echo "[4/5] Opening the ports in the firewall ..."
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi "^Status: active"; then
    if ufw allow "${PORT_START}:${PORT_END}/tcp" >>"$LOG" 2>&1; then
        echo "      ufw: opened ${PORT_START}-${PORT_END}/tcp."
    else
        echo "      [WARN] Could not open the range in ufw. See app/logs/install.log"
    fi
elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    if firewall-cmd --permanent --add-port="${PORT_START}-${PORT_END}/tcp" >>"$LOG" 2>&1 \
       && firewall-cmd --reload >>"$LOG" 2>&1; then
        echo "      firewalld: opened ${PORT_START}-${PORT_END}/tcp."
    else
        echo "      [WARN] Could not open the range in firewalld. See app/logs/install.log"
    fi
else
    echo "      No active firewall. Nothing to do."
fi

echo "[5/5] Checking the open files limit ..."
# Each emulated device opens a listening socket and keeps connections open.
# Twice the number of ports plus 1024 covers the concurrent connections.
NEEDED=$((PORT_COUNT * 2 + 1024))

# The script runs under sudo, but the application is later started by the
# regular user through ./start.sh. That user's limit is the one that matters.
TARGET_USER="${SUDO_USER:-}"
if [ -z "$TARGET_USER" ]; then
    TARGET_USER="$(logname 2>/dev/null || true)"
fi

CURRENT=""
if [ -n "$TARGET_USER" ]; then
    CURRENT="$(su - "$TARGET_USER" -c 'ulimit -n' 2>/dev/null || true)"
fi
if ! echo "${CURRENT:-}" | grep -qE '^[0-9]+$'; then
    CURRENT="$(ulimit -n)"
    TARGET_USER="root"
fi

if [ "$CURRENT" -lt "$NEEDED" ]; then
    cat > /etc/security/limits.d/facial-device-emulator.conf <<LIMITS
# Written by the Facial Device Emulator install.sh for $PORT_COUNT devices.
* soft nofile $NEEDED
* hard nofile $NEEDED
LIMITS
    echo "      Current limit of user $TARGET_USER ($CURRENT) is below the required $NEEDED."
    echo "      [WARN] Raised in /etc/security/limits.d/facial-device-emulator.conf."
    echo "             Log out and back in before running ./start.sh."
else
    echo "      Current limit of user $TARGET_USER ($CURRENT) is enough."
fi

if [ "$ON_WSL" -eq 1 ] && [ ! -e /sys/class/net/loopback0 ]; then
    echo
    echo "[WARN] This WSL instance is not in mirrored networking mode."
    echo "       Without it, only THIS machine can reach the emulated devices:"
    echo "       an access control system on another computer cannot connect."
    echo
    echo "       To fix it, create or edit .wslconfig in your Windows user"
    echo "       folder (C:\\Users\\<you>\\.wslconfig) with:"
    echo
    echo "         [wsl2]"
    echo "         networkingMode=mirrored"
    echo
    echo "       Then, in Windows PowerShell: wsl --shutdown"
    echo "       and open WSL again."
    echo
fi

echo
echo "[OK] Installed. Run ./start.sh"
