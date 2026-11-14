# Configuration

The service reads one YAML file and then applies environment overrides. The order is:

1. `.env.local`, `.env` or `.env.development` in the working directory (the first one
   found). Variables already set in the real environment are not overwritten.
2. The YAML file given by `-config` (default `configs/config.yaml`).
3. Environment variable overrides.

## YAML file

```yaml
app_version: "1.0.0"     # shown in the console header

server:
  host: "0.0.0.0"         # listen address of the console, REST API and monitoring
  port: 7070              # their port; devices use their own ports

service_db:
  driver: "postgres"      # must be "postgres"
  host: "127.0.0.1"
  port: 5432
  username: "emulator"
  password: "emulator123"
  database: "emulator_db"
  schema: ""              # optional, appended as search_path
  reset_on_mismatch: false # see "Database ownership"; true DROPS DATA

emulator:
  default_timezone: "UTC" # IANA zone devices report times in (see below)

log:
  level: "info"           # debug, info, warn or error
  format: "text"          # text or json
  file: ""                # optional file, written besides stdout
  max_size_mb: 10         # rotation size of the file
  max_files: 5            # rotated copies kept (trace.log.1 ... trace.log.5)
```

The connection URL is built from these fields with every part escaped, so the user
name and password may contain any character (`@`, `:`, `/`, `#`, ...). A `driver` other
than `postgres`, or an empty `host` or `database`, stops the service at startup.

The service opens two logical connections to this database: one for fleet management
(`service` schema) and one for per-device state (`emulator` schema). Both use the
`service_db` settings; an `emulator_db` block in the file, if present, is ignored.

`emulator.default_timezone` is the IANA time zone (for example `UTC`,
`America/New_York`, `Asia/Tokyo`) every device reports local time in: event timestamps
(written with the matching numeric offset, such as `-05:00`), the device clock and the
event-history queries. It does not depend on the host's time zone. An unknown name stops
the service at startup. A Hikvision device switches to the zone the client sets with
`PUT /ISAPI/System/time` (`<timeZone>`, POSIX style such as `CST-8:00:00` for UTC+08:00;
DST rules are ignored); this is kept in memory until the device restarts. Dahua devices
always use the default.

## Logging

The service log goes to stdout. `log.format: text` writes `key=value` lines
(`time=... level=INFO msg="..."`); `json` writes one JSON object per line, for log
collectors. `log.level` drops lines below that level. When `log.file` is set (relative to
the working directory) every line is also appended to that file; when it would pass
`max_size_mb` it is renamed to `<file>.1` (older copies shift to `.2`, `.3`, ...) and a
new file starts, keeping `max_files` copies. The Linux and Windows packages and the
compose files set `logs/trace.log`.

Keys not listed above (for example `sslmode`, `max_connections`) are not read.
Pool sizes are computed from the number of devices; see
[architecture.md](architecture.md#database-layer).

## Environment variables

| Variable | Overrides | Notes |
|---|---|---|
| `SERVER_HOST` | `server.host` | |
| `SERVER_PORT` | `server.port` | |
| `DB_HOST` | `service_db.host` | Also `PG_HOST`, `POSTGRES_HOST` |
| `DB_PORT` | `service_db.port` | Also `PG_PORT`, `POSTGRES_PORT` |
| `DB_USER`, `DB_USERNAME` | `service_db.username` | Also with `PG_` / `POSTGRES_` |
| `DB_PASSWORD` | `service_db.password` | Also with `PG_` / `POSTGRES_` |
| `DB_DATABASE`, `DB_DB` | `service_db.database` | Also with `PG_` / `POSTGRES_` |
| `DB_DRIVER` | `service_db.driver` | |
| `DB_SCHEMA` | `service_db.schema` | |
| `DB_RESET_ON_MISMATCH` | `service_db.reset_on_mismatch` | `true`/`false`. Also with `PG_` / `POSTGRES_` |
| `EMULATOR_DEFAULT_TIMEZONE` | `emulator.default_timezone` | IANA name, default `UTC` |
| `LOG_LEVEL` | `log.level` | |
| `LOG_FORMAT` | `log.format` | |
| `LOG_FILE` | `log.file` | |
| `USE_DUAL_POOL_MANAGER` | - | `true` splits reads and writes into two pools with an ordered write queue |
| `PUBLISHED_PORT_RANGE` | - | Docker only: the host ports published for devices, such as `4000-4999` or `4000-4099,4500` |
| `HOST_NETWORK` | - | Docker only: `1` when the container runs with `network_mode: host` |

When several prefixes are set, they are applied in the order `PG_`, `POSTGRES_`, `DB_`,
so `DB_*` wins. A port that is not a number (`SERVER_PORT`, `DB_PORT`, ...) stops the
service at startup instead of being ignored. Inside the official PostgreSQL image `POSTGRES_USER`, `POSTGRES_PASSWORD`
and `POSTGRES_DB` configure the server; they only affect the emulator if they are also
set in the emulator's own environment.

`PUBLISHED_PORT_RANGE` and `HOST_NETWORK` do not change what the service does. They tell
the reachability check what the container exposes, so the console can warn about devices
that will start but cannot be reached. See
[ports-and-networking.md](ports-and-networking.md).

## Command-line flags

| Flag | Default | Purpose |
|---|---|---|
| `-config` | `configs/config.yaml` | Path to the YAML file |
| `-reset-db` | off | If the database structure does not match, drop and recreate the `service` and `emulator` schemas. **Deletes all their data** |

## Per-device settings

Device properties are stored in the database, not in the config file. Each device has:

| Field | Set by | Meaning |
|---|---|---|
| `name` | API / console | Display name |
| `model` | API / console | `Hikvision` or `Dahua` |
| `port` | API / console | TCP port the device listens on (1-65535, unique) |
| `ip_address` | API / console | Reported in the device's own responses and events; the listener always binds to all interfaces. Default `127.0.0.1` |
| `event_interval` | API / console | Seconds between generated events. Default 10 |
| `enabled` | API / console | Disabled devices cannot start |
| `log_enabled` | API / console | Per-device request log on or off |
| mode | API / console (Dahua), the system under test (both) | `online` or `standalone`, stored as the `LocalAuthentication` device setting |

Everything else a device "knows" (users, cards, faces, fingerprints, event server
address, door parameters, vendor config keys) is written by the access control system
through the device protocol, exactly as on real hardware, and is visible in the console
drawer and in `GET /api/devices/:id/settings`.

## Files written at runtime

| Path | Content |
|---|---|
| `log.file` (for example `logs/trace.log`) | Service log, only when `log.file` is set; rotated as described in [Logging](#logging) |
| `logs/devices/device-<id>.log` | Per-device request log when enabled, rotated at 3 MB, 8 files kept; deleted when the log is turned off |

`logs/` is relative to the working directory.

## Database ownership

The service owns the `service` and `emulator` schemas of its database. On startup it
checks their structure:

- none of its tables exist: it creates both schemas and all tables (in one transaction);
- all tables and their key columns exist: it starts;
- anything in between (some tables missing, a key column missing, for example a database
  created by an incompatible version or changed by hand): it **refuses to start** and
  logs what is missing. Nothing is changed.

To recover from the last case, fix the schema by hand, point the service at an empty
database, or start it once with `-reset-db` (or `service_db.reset_on_mismatch: true`,
`DB_RESET_ON_MISMATCH=true`). **That drops the `service` and `emulator` schemas with all
devices and their data** and recreates them; leave the setting off afterwards. Use a
dedicated database (the default `emulator_db`), never one shared with another
application.
