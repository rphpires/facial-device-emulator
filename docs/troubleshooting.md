# Troubleshooting

Start with the logs:

| Install | Service log | Device logs |
|---|---|---|
| Docker Compose (repository) | `docker compose logs app`, `./logs/trace.log` | `./logs/devices/` |
| Docker package | `app/logs/trace.log` | `app/logs/devices/` |
| Linux package | `app/logs/trace.log`, `app/logs/app.out` | `app/logs/devices/` |
| Windows package | `app\logs\trace.log`, `app\logs\postgres.log` | `app\logs\devices\` |
| From source | stdout (set `log.file` or `LOG_FILE` for a file) | `logs/devices/` |

Rotated copies of the service log are `trace.log.1`, `trace.log.2`, ... For more detail
set `LOG_LEVEL=debug`; for a log collector, `LOG_FORMAT=json`. See
[configuration.md](configuration.md#logging).

## The service does not start

**`Failed to validate/recreate database` or `connection refused`.** PostgreSQL is not
reachable with the configured host, port, user and password. Check the effective values
(environment variables override the YAML, see [configuration.md](configuration.md)) and
test them:

```bash
PGPASSWORD=emulator123 psql -h 127.0.0.1 -p 5432 -U emulator -d emulator_db -c 'select 1'
```

**`database "emulator_db" does not exist`.** The role exists but the database does not.
With Docker Compose it is created by `docker-init.sql`, which only runs when the data
volume is created for the first time; recreate the volume with
`docker compose down -v && docker compose up -d`. Natively, run
`sudo bash scripts/setup-postgres-local.sh`.

**`bind: address already in use` on 7070.** Another process uses the console port. Change
it with `SERVER_PORT` (natively) or `WEB_PORT` (Docker Compose).

**Docker Compose fails with "port is already allocated".** A host port in
`DEVICE_PORT_RANGE` or `WEB_PORT` is taken. Pick free ones:
`WEB_PORT=17070 DEVICE_PORT_RANGE=5000-5999 docker compose up -d`.

**"database structure does not match".** The `service` and `emulator` schemas exist but
are incomplete (some tables or key columns missing), for example a database created by
an incompatible version or changed by hand. The service does not touch them and stops;
the log lists what is missing. Fix the schema, point the service at an empty database,
or start it once with `-reset-db` (or `DB_RESET_ON_MISMATCH=true`), which **drops both
schemas with all devices and their data** and recreates them. See
[configuration.md](configuration.md#database-ownership).

**The data disappeared after a restart.** Older versions dropped and recreated the
schemas automatically on a mismatch; this one only does so with `-reset-db` or
`reset_on_mismatch: true`. Check that neither is left on, and that the service points at
the database you expect (`DB_HOST`, `DB_DATABASE`).

## A device does not start

The console shows the device as `error`; hover the status for the reason, or read
`last_error` in the `/events` stream or the `start_error` of the create call.

| Reason | Fix |
|---|---|
| `address already in use` | Another process (or another emulator instance) owns the port. Change the device port or stop that process |
| `permission denied` | Port below 1024 on Linux. Use a higher port |
| `too many open files` | Raise the open files limit, see [load-testing.md](load-testing.md#open-files) |
| `timeout starting emulator N after 10 seconds` | The host is overloaded during a mass start; retry, or start in smaller batches |
| `device N is disabled` | Enable it first (`enable` batch action or edit) |

## The access control system cannot reach a device

1. Check the warning above the device table, or `GET /api/reachability`.
2. Test from the machine running the access control system:
   `curl http://<emulator-host>:<port>/ISAPI/System/deviceInfo` (Hikvision) or
   `curl 'http://<emulator-host>:<port>/cgi-bin/magicBox.cgi?action=getSoftwareVersion'`
   (Dahua).
3. Common causes:
   - **Docker:** the port is outside the published range. Widen `DEVICE_PORT_RANGE` or
     move the device.
   - **WSL2:** mirrored networking is off, so only the local machine can connect.
   - **Firewall:** the range is not allowed (ufw, firewalld, Windows Firewall).
   - **Wrong host:** the system must connect to the address of the machine running the
     emulator; the device's `ip_address` field is cosmetic.

See [ports-and-networking.md](ports-and-networking.md).

## Devices run but no events arrive

- The device has no users with cards: events are generated only from enrolled
  credentials. Check the drawer's Users tab or `GET /api/devices/:id/users`.
- **Standalone mode:** events are only delivered on an open event stream
  (`/ISAPI/Event/notification/alertStream`, `snapManager.cgi`, `eventManager.cgi`). Make
  sure the system under test keeps one open.
- **Online mode:** events are pushed to the server address the system configured on the
  device. Check `GET /api/devices/:id/settings` for the stored address, and turn on the
  device log to see each outgoing push and its result.
- The mode is what you expect: `GET /api/devices/:id/mode`.

## The console does not update

The header meter shows `reconnecting...` or `offline` when the SSE stream to `/events`
is down. A reverse proxy in front of the service must not buffer `text/event-stream`
responses (for nginx: `proxy_buffering off;`). The stream sends a keepalive comment every
20 seconds.

## "sorry, too many clients already"

PostgreSQL ran out of connections under load. Raise `max_connections` (see
[load-testing.md](load-testing.md#4-database)), and restart the emulator after creating
a large fleet so its pools are sized from the right device count.

## Large "Start all" ends with a 500

With `?wait=true`, `failed to start N/M devices` means some devices still failed after 3
attempts. The trace log lists each failure with its reason. Without `?wait=true` the call
returns `202` immediately and failures are reported per device in the console.

## `docker compose stop` takes about 30 seconds

The service itself shuts down in well under a second after SIGTERM (check the
`Server exited` line in the logs). The delay is Docker tearing down the published
device port range: with the default `DEVICE_PORT_RANGE=4000-4999`, Docker runs one
`docker-proxy` per port. Publish a smaller range if you don't need 1000 devices,
for example `DEVICE_PORT_RANGE=4000-4199 docker compose up -d`, or set
`"userland-proxy": false` in the Docker daemon configuration.

## Collecting information for a bug report

```bash
curl -s http://localhost:7070/monitoring/health
curl -s http://localhost:7070/api/reachability
curl -s http://localhost:7070/api/pool-stats
curl -s 'http://localhost:7070/monitoring/metrics/errors?limit=20'
```

plus `logs/trace.log` and, for a specific device, its log zip from
`GET /api/devices/:id/log.zip`.
