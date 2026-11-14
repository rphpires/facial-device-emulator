#!/usr/bin/env bash
set -uo pipefail
cd "$(dirname "$0")"

PIDFILE=app/logs/app.pid

echo "Stopping the application ..."
if [ -f "$PIDFILE" ]; then
    kill "$(cat "$PIDFILE")" 2>/dev/null
    rm -f "$PIDFILE"
else
    pkill -f "facial-device-emulator -config" 2>/dev/null
fi

echo
echo "[OK] Stopped. Data is kept."
echo "     The database keeps running (it is a system service)."
