#!/usr/bin/env bash
set -uo pipefail
cd "$(dirname "$0")"

mkdir -p app/logs
PIDFILE=app/logs/app.pid

echo "=============================================================="
echo "  Facial Device Emulator - Start"
echo "=============================================================="

if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    echo "[ERROR] Already running. Open http://localhost:7070 or run ./stop.sh first."
    exit 1
fi

echo "[1/3] Starting the database ..."
if grep -qiE "microsoft|wsl" /proc/version 2>/dev/null; then
    sudo service postgresql start >/dev/null 2>&1
else
    sudo systemctl start postgresql >/dev/null 2>&1
fi

echo "[2/3] Starting the application ..."
cd app
nohup ./facial-device-emulator -config configs/config.yaml >logs/app.out 2>&1 &
echo $! > logs/app.pid
cd ..

echo "[3/3] Waiting for the application to answer ..."
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
echo "        See app/logs/trace.log and app/logs/app.out"
exit 1
