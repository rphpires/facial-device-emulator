# Getting started

There are four ways to run the emulator. All of them end with the web console on
http://localhost:7070 and the emulated devices on their own ports.

| Option | Best for | Needs |
|---|---|---|
| [Docker Compose from the repository](#docker-compose-from-the-repository) | trying it out, CI, development | Docker with Compose v2 |
| [Docker package](#docker-package) | a test machine without the source tree | Docker |
| [Linux package](#linux-package) | native Linux or WSL2, no Docker | sudo, apt |
| [Windows package](#windows-package) | Windows without Docker | nothing (PostgreSQL is bundled) |
| [From source](#from-source) | development | Go 1.21+, PostgreSQL |

## Docker Compose from the repository

```bash
git clone https://github.com/rphpires/facial-device-emulator.git
cd facial-device-emulator
docker compose up -d --build --wait     # or: make docker-up
```

This starts two containers:

- `facial-device-emulator-db`: PostgreSQL 15, not published on the host.
- `facial-device-emulator-app`: the service, publishing the console on `7070` and the
  device ports `4000-4999`.

Two environment variables change the host ports without editing the file:

| Variable | Default | Effect |
|---|---|---|
| `WEB_PORT` | `7070` | Host port of the console and REST API |
| `DEVICE_PORT_RANGE` | `4000-4999` | Host ports published for devices; also sets `PUBLISHED_PORT_RANGE` |

```bash
# Console on 17070, only 100 device ports (starts faster on Docker Desktop)
WEB_PORT=17070 DEVICE_PORT_RANGE=4000-4099 docker compose up -d --build --wait
```

### First run, step by step

These are the exact steps used to verify the quick start (console moved to 17070 because
7070 was taken on the test machine):

```bash
WEB_PORT=17070 docker compose -p fdeverify up -d --build --wait
B=http://localhost:17070

curl -s $B/monitoring/health/quick                     # {"status":"healthy",...}

curl -s -X POST $B/api/emulators/range -H 'Content-Type: application/json' \
  -d '{"name_prefix":"hik","model":"Hikvision","port_start":4000,"port_end":4019}'
curl -s -X POST $B/api/emulators/range -H 'Content-Type: application/json' \
  -d '{"name_prefix":"dahua","model":"Dahua","port_start":4100,"port_end":4109}'
curl -s -X POST $B/api/emulators -H 'Content-Type: application/json' \
  -d '{"name":"lobby-1","model":"Hikvision","port":4500,"auto_start":true}'

curl -s -X POST "$B/api/emulators/start?wait=true"     # {"status":"started"}
curl -s $B/api/status                                  # 31 total, 31 running

curl -s http://localhost:4000/ISAPI/System/deviceInfo  # <DeviceInfo ...> XML
curl -s 'http://localhost:4100/cgi-bin/magicBox.cgi?action=getSoftwareVersion'

# Enroll a user the way an access control system would, then read it back
curl -s -X POST 'http://localhost:4000/ISAPI/AccessControl/UserInfo/Record?format=json' \
  -H 'Content-Type: application/json' \
  -d '{"UserInfo":{"employeeNo":"1001","name":"Test User","userType":"normal",
       "Valid":{"enable":true,"beginTime":"2026-01-01T00:00:00","endTime":"2030-12-31T23:59:59"}}}'
curl -s $B/api/devices/1/users

# Open the console in a browser: http://localhost:17070/

docker compose -p fdeverify down -v
```

Stop with `docker compose down`; add `-v` to delete the database volume as well.
Logs are written to `./logs` on the host (bind mount).

## Docker package

The Docker package is a ZIP with the image already built (`docker save`), compose files
and start/stop scripts, for machines that should not build from source. Build it with
`packaging/build-packages.sh docker` (or `.bat` on Windows); the ZIP lands in
`packaging/.out/facial-device-emulator-docker.zip`.

On the target machine, unzip it and:

| Step | Windows / macOS (Docker Desktop) | Linux |
|---|---|---|
| Install (first time) | `install.bat` | `./install.sh` |
| Start | `start.bat` | `./start.sh` |
| Stop | `stop.bat` | `./stop.sh` |

`install` loads the image and starts PostgreSQL, waiting for its healthcheck. `start`
brings the app up and waits until `/monitoring/health/quick` answers.

On native Linux the scripts use `app/docker-compose.linux.yml`, which runs the app with
`network_mode: host`: every device port is reachable and there is no published range.
On Docker Desktop and WSL they use `app/docker-compose.yml`, which publishes
`4000-4499`. See [ports-and-networking.md](ports-and-networking.md).

## Linux package

A ZIP with the static binary, a config file and three scripts. Works on Debian/Ubuntu and
on WSL2. Build it with `packaging/build-packages.sh linux`.

```bash
unzip facial-device-emulator-linux.zip -d facial-device-emulator
cd facial-device-emulator
sudo ./install.sh     # first time only
./start.sh
./stop.sh
```

`install.sh`:

1. Installs PostgreSQL with apt if `psql` is missing, and starts it (`systemctl`, or
   `service` on WSL).
2. Creates the `emulator` role and the `emulator_db` database.
3. Asks for the device port range (default `4000-4499`) and opens it in `ufw` or
   `firewalld` when one is active.
4. Raises the open files limit in `/etc/security/limits.d/facial-device-emulator.conf`
   when the current limit is below `2 x ports + 1024`. Log out and back in afterwards.
5. On WSL without mirrored networking, prints how to enable it.

`start.sh` runs the binary in the background (`app/logs/app.pid`) and waits for the
health endpoint. `stop.sh` stops the binary; PostgreSQL keeps running as a system service.

## Windows package

A ZIP with `facial-device-emulator.exe`, a portable PostgreSQL 15 and three scripts. No
Docker, no installer. Build it with `packaging\build-packages.bat windows` (or the `.sh`
variant; it downloads the portable PostgreSQL once into `.build-cache/`).

1. Double-click `install.bat` (first time only). It creates a database cluster in
   `app\postgres\data`, listening on `127.0.0.1:5433`, and the `emulator` role and
   database.
2. Double-click `start.bat`. It starts PostgreSQL and the service and waits for the
   health endpoint.
3. Open http://localhost:7070.
4. `stop.bat` stops both.

Windows Firewall may block the device ports; allow the range the devices use.

## From source

Requirements: Go 1.21+ and a PostgreSQL server.

```bash
make db-up    # PostgreSQL 15 in Docker on 127.0.0.1:5432
make run      # go run ./cmd/emulator-service -config configs/config.yaml
```

If port 5432 is taken, pick another one for both sides:

```bash
POSTGRES_HOST_PORT=5544 make db-up
DB_PORT=5544 make run
```

For a native PostgreSQL on Debian/Ubuntu, `sudo bash scripts/setup-postgres-local.sh`
creates the role and the database and applies tuning for many concurrent devices
(see [load-testing.md](load-testing.md)).

On startup the service validates the schema, applies the embedded migrations and resets
every device to `stopped`. Devices are not started automatically: use the console or
`POST /api/emulators/start`.

`make help` lists the other targets (`build`, `test`, `test-race`, `vet`, `lint`,
`docker-up`, `docker-down`, `packages`).
