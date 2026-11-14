#!/bin/bash
# Native PostgreSQL setup for running the emulator from source (Debian/Ubuntu).
# Idempotent: safe to run again.
# Run with: sudo bash scripts/setup-postgres-local.sh
set -euo pipefail

DB_USER="emulator"
DB_PASS="emulator123"
DB_NAME="emulator_db"

echo "==> 1. Installing PostgreSQL..."
if ! command -v psql >/dev/null 2>&1; then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y postgresql postgresql-contrib
else
    echo "    PostgreSQL already installed, skipping apt."
fi

# Detect the installed version/cluster
PG_VER=$(ls /etc/postgresql/ | sort -n | tail -1)
echo "==> Cluster detected: version $PG_VER"

echo "==> 2. Making sure the cluster is running..."
pg_ctlcluster "$PG_VER" main start 2>/dev/null || true
systemctl enable postgresql 2>/dev/null || true
# Wait until it accepts connections
for i in $(seq 1 15); do
    if pg_isready -q; then break; fi
    sleep 1
done

echo "==> 3. Creating role '$DB_USER' and database '$DB_NAME'..."
sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
DO \$\$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${DB_USER}') THEN
        CREATE ROLE ${DB_USER} LOGIN PASSWORD '${DB_PASS}' CREATEDB;
    ELSE
        ALTER ROLE ${DB_USER} LOGIN PASSWORD '${DB_PASS}' CREATEDB;
    END IF;
END
\$\$;
SQL

# Create the database if missing (CREATE DATABASE cannot run in a DO block)
if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'" | grep -q 1; then
    sudo -u postgres createdb -O "${DB_USER}" "${DB_NAME}"
    echo "    Database ${DB_NAME} created."
else
    echo "    Database ${DB_NAME} already exists."
fi
sudo -u postgres psql -v ON_ERROR_STOP=1 -c "ALTER DATABASE ${DB_NAME} OWNER TO ${DB_USER};"

echo "==> 4. Applying tuning (ALTER SYSTEM)..."
# Sized for a 16 GB / 10-core workstation running many emulated devices that
# issue many small queries. max_connections is high because the service
# opens two connection pools (service and emulator), which doubles the
# connection count. Adjust the memory values to your machine.
sudo -u postgres psql -v ON_ERROR_STOP=1 <<'SQL'
ALTER SYSTEM SET max_connections = 400;
ALTER SYSTEM SET shared_buffers = '4GB';
ALTER SYSTEM SET effective_cache_size = '10GB';
ALTER SYSTEM SET work_mem = '8MB';
ALTER SYSTEM SET maintenance_work_mem = '512MB';
ALTER SYSTEM SET synchronous_commit = 'off';
ALTER SYSTEM SET wal_buffers = '16MB';
ALTER SYSTEM SET max_wal_size = '4GB';
ALTER SYSTEM SET min_wal_size = '1GB';
ALTER SYSTEM SET checkpoint_completion_target = '0.9';
ALTER SYSTEM SET random_page_cost = 1.1;
ALTER SYSTEM SET effective_io_concurrency = 200;
SQL

echo "==> 5. Restarting to apply max_connections/shared_buffers..."
pg_ctlcluster "$PG_VER" main restart
for i in $(seq 1 15); do
    if pg_isready -q; then break; fi
    sleep 1
done

echo "==> 6. Testing a TCP connection as '$DB_USER'..."
if PGPASSWORD="${DB_PASS}" psql -h localhost -U "${DB_USER}" -d "${DB_NAME}" -c "SELECT 'connection OK' AS status, current_setting('max_connections') AS max_conns;"; then
    echo ""
    echo "==> SETUP COMPLETE. Database ready."
    echo "    Start the service with: make run"
else
    echo "!! TCP connection failed. Check pg_hba.conf (localhost auth)."
    exit 1
fi
