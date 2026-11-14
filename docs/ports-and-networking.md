# Ports and networking

## Ports used

| Port | Who listens | Configured by |
|---|---|---|
| `7070` | Console, REST API, `/events`, `/monitoring` | `server.port` / `SERVER_PORT` |
| One per device | The device's ISAPI or CGI server | The device's `port` field |
| `5432` | PostgreSQL | `service_db.port` / `DB_PORT` (`5433` in the Windows package) |

Each device listens on `0.0.0.0:<port>`. The `ip_address` of a device is only reported
in its responses and events; it does not change the bind address. Several devices
therefore share one host, distinguished by port: the system under test registers them
as `host:4000`, `host:4001`, and so on.

The service never picks or changes a device port. It rejects, at creation time, a port
already used by another device or equal to its own `server.port`, and a range wider
than 1000 ports (`MaxRangeSize`). Whether the port can actually be opened and reached is
checked at start time and by the reachability check below.

## Docker

### Published range (default)

A container's ports are only reachable from outside if Docker publishes them. The
repository's `docker-compose.yml` publishes `4000-4999` by default, which matches the
1000-port limit of a range:

```yaml
environment:
  PUBLISHED_PORT_RANGE: "${DEVICE_PORT_RANGE:-4000-4999}"
ports:
  - "${WEB_PORT:-7070}:7070"
  - "${DEVICE_PORT_RANGE:-4000-4999}:${DEVICE_PORT_RANGE:-4000-4999}"
```

A device configured on a port outside the published range starts normally inside the
container, but nothing outside can connect to it. `PUBLISHED_PORT_RANGE` tells the
service what is published, so the console can flag those devices before they are even
started. Keep the two in sync: in the repository compose file both come from
`DEVICE_PORT_RANGE`; in `packaging/docker/docker-compose.yml` they are written out, and a
unit test (`internal/reachability/compose_test.go`) fails if they diverge.

To change the range:

```bash
DEVICE_PORT_RANGE=5000-5499 docker compose up -d
```

`PUBLISHED_PORT_RANGE` accepts a comma-separated list of ports and ranges, for example
`4000-4099,4500`.

Publishing fewer ports makes `docker compose up` faster on Docker Desktop, where each
published port has a cost.

### Host networking (native Linux)

On Docker Engine for Linux, `network_mode: host` gives the container the host's network
stack: every device port is reachable directly, with no published range and no limit.
The Linux Docker package uses `packaging/docker/docker-compose.linux.yml`, which does
this and sets `HOST_NETWORK=1` so the reachability check knows. In that file PostgreSQL
is bound to `127.0.0.1:55432` to avoid clashing with a PostgreSQL on the host.

Host networking is not reliable on Docker Desktop (Windows and macOS); use the published
range there.

## WSL2

By default WSL2 runs behind NAT: services inside it are reachable from the Windows host
through `localhost` forwarding, but **not** from other machines on the network. An
access control system on another computer will not reach the emulated devices.

Enable mirrored networking in `%USERPROFILE%\.wslconfig` on Windows:

```ini
[wsl2]
networkingMode=mirrored
```

Then run `wsl --shutdown` in PowerShell and open WSL again. The service detects mirrored
mode by the presence of the `loopback0` interface (`/sys/class/net/loopback0`). Windows
Firewall must also allow the device ports.

## Native Linux and Windows

There is no published range: any port the operating system lets the service open is
reachable, subject to the firewall.

- Linux: `packaging/linux/install.sh` opens the chosen range in `ufw` or `firewalld` if
  one is active. Manually: `sudo ufw allow 4000:4499/tcp`.
- Windows: allow the range in Windows Firewall, for example (PowerShell, as
  administrator):
  `New-NetFirewallRule -DisplayName "Facial Device Emulator" -Direction Inbound -Protocol TCP -LocalPort 4000-4499 -Action Allow`

Ports below 1024 need elevated privileges on Linux; stay above 1024.

## Reachability check

`GET /api/reachability` (and the warning in the console) classifies each device. The
environment is detected once at startup:

| Environment | Detected by |
|---|---|
| `docker` | `/.dockerenv` exists |
| `wsl` | `/proc/version` mentions Microsoft or WSL |
| `windows` | Go runtime OS |
| `linux` | anything else |

Rules, in order:

1. A device whose last start failed to open its port is `unreachable`, in any
   environment, with the operating system error as the reason.
2. Docker without host networking: `ok` if the port is inside `PUBLISHED_PORT_RANGE`,
   `unreachable` if not, `unknown` if the variable is missing or invalid. This applies
   even to devices that were never started, so the warning shows up before the test.
3. WSL without mirrored networking: `unreachable` ("only this machine can reach port N").
4. Otherwise: `ok` once the device has started, `not_started` before that.

The report also includes `max_open_files` (the process's `RLIMIT_NOFILE` soft limit on
Linux, `0` on Windows) to help diagnose large fleets; see
[load-testing.md](load-testing.md).

## Checking from the outside

```bash
# From the machine running the access control system
curl http://<emulator-host>:4000/ISAPI/System/deviceInfo
curl 'http://<emulator-host>:4100/cgi-bin/magicBox.cgi?action=getSoftwareVersion'

# Docker: what is published
docker port facial-device-emulator-app
```
