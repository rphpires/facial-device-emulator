# Load testing with a large fleet

The point of the emulator is to put an access control system in front of hundreds of
terminals at once. This page covers how to build such a fleet and which limits of the
host matter.

## 1. Create the fleet

A range creates up to 1000 devices in one call and in one transaction. For a mixed fleet,
create several ranges:

```bash
API=http://localhost:7070/api/emulators/range
curl -X POST $API -H 'Content-Type: application/json' \
  -d '{"name_prefix":"hik","model":"Hikvision","port_start":4000,"port_end":4599,"event_interval":5}'
curl -X POST $API -H 'Content-Type: application/json' \
  -d '{"name_prefix":"dahua","model":"Dahua","port_start":4600,"port_end":4999,"event_interval":5}'
```

There is no fleet-wide cap other than ports and host resources. Under Docker, every
device port must be inside the published range (`DEVICE_PORT_RANGE`, default
`4000-4999`); for more than 1000 devices, widen it or use host networking (see
[ports-and-networking.md](ports-and-networking.md)).

**Restart the service after creating a large fleet.** Database pools are sized at startup
from the number of enabled devices (see [Database](#4-database) below). They also grow on
demand, but starting with the right size avoids expansions during the test.

## 2. Start it

```bash
curl -X POST http://localhost:7070/api/emulators/start          # 202, runs in background
watch -n1 'curl -s http://localhost:7070/api/status'
```

"Start all" starts 20 devices at a time, retries each failed start up to 3 times, and
pauses the watchdog until 15 seconds after the ramp. Each start has a 10-second timeout.
With `?wait=true` the request blocks until the end (bounded by the 5-minute server write
timeout), which is handy in scripts for fleets that come up within that time.

To start or stop a subset, use the batch endpoint (32 concurrent, 5-minute budget, up to
1000 IDs per call):

```bash
curl -X POST http://localhost:7070/api/emulators/batch -H 'Content-Type: application/json' \
  -d '{"ids":[1,2,3,4,5],"action":"start"}'
```

## 3. Operating system limits

### Open files

Every device holds a listening socket, plus one socket per open connection: the
long-lived event stream the access control system keeps per device, request
connections, outgoing event pushes, and the database pool. A fleet of N devices needs
comfortably more than N file descriptors. The Linux package installer uses
`2 x ports + 1024` as its target.

Check the limit the service actually runs with:

```bash
curl -s http://localhost:7070/api/reachability | jq .environment.max_open_files
```

Raise it when it is too low:

- Shell: `ulimit -n 8192` before starting the binary.
- Persistent (PAM sessions): a file in `/etc/security/limits.d/`, as the Linux package
  installer writes:
  ```
  * soft nofile 3024
  * hard nofile 3024
  ```
  Log out and back in afterwards.
- systemd unit: `LimitNOFILE=8192`.
- Docker: containers usually inherit a high limit (the verification run reported
  524288); otherwise set `ulimits: { nofile: 8192 }` on the `app` service.

When the limit is hit, starts fail with "too many open files" and the device shows
`error` with that reason in the console.

### Ports

Stay above 1024 (no privileges needed) and away from the ephemeral range the operating
system uses for outgoing connections (Linux: `cat /proc/sys/net/ipv4/ip_local_port_range`,
typically `32768-60999`). The default `4000-4999` satisfies both.

## 4. Database

Each emulated device reads and writes its own state (users, cards, faces, events,
settings) on every protocol call, so PostgreSQL sees the fleet's full request rate.

**Connection pools.** The service opens two pools against the same database (fleet
management and per-device state). At startup it counts the enabled devices `n` and sizes
each pool once, as follows. Connections are opened lazily, only when queries need them,
and closed after 5 minutes idle, so this is a ceiling, not what is held open:

| | Formula | n = 100 | n = 300 | n = 1000 |
|---|---|---|---|---|
| Max connections | `3 x (2n + 5)` | 615 | 1815 | 6015 |

The pool is not resized while running; restart the service after adding many devices so
it counts them again.

**PostgreSQL `max_connections`.** The default of 100 is enough for small fleets, but a
busy fleet of hundreds of devices can exceed it ("sorry, too many clients already").
Raise it:

- Native: `scripts/setup-postgres-local.sh` sets `max_connections = 400` and tuning for
  many small queries (`shared_buffers`, `synchronous_commit = off`, WAL sizes). Adjust
  the memory values to the machine.
- Docker: add a command to the `postgres` service, for example
  `command: ["postgres", "-c", "max_connections=400", "-c", "synchronous_commit=off"]`.

**Read/write split.** With `USE_DUAL_POOL_MANAGER=true` each logical connection becomes a
read pool plus a small write pool fed by an ordered write queue that batches writes and
preserves per-device order. Sizes for the same fleets:

| | n = 100 | n = 300 | n = 1000 |
|---|---|---|---|
| Read pool min / max | 30 / 300 | 90 / 900 | 300 / 3000 |
| Write pool min / max | 10 / 12 | 18 / 22 | 32 / 39 |
| Write workers | 10 | 18 | 32 |
| Write queue size | 1000 | 1500 | 5000 |
| Write batch size | 20 | 25 | 30 |
| Flush interval | 2500 ms | 2019 ms | 1667 ms |

The startup log prints the computed configuration and a recommended `max_connections`.

## 5. Event volume

Each running device generates one access event every `event_interval` seconds (default
10), picked from the users and cards the system under test enrolled on it. A device with
no enrolled users generates nothing. Events go out:

- in standalone mode, on the event stream the system keeps open
  (`alertStream`, `snapManager.cgi`, `eventManager.cgi`), with heartbeats in between;
- in online mode, as a push to the server address the system configured (Hikvision
  `httpHosts`, Dahua upload server), randomly followed by door open/close events.

The aggregate event rate is roughly `running devices / event_interval` per second; 1000
devices at a 5-second interval means about 200 events per second hitting the system
under test, most of them carrying a JPEG picture as real devices do.

## 6. Watch the emulator, not just the system under test

The emulator should never be the bottleneck. During a run, keep an eye on:

| Endpoint | Look for |
|---|---|
| `/api/pool-stats` | `acquired_conns` close to `max_conns`, `empty_acquire_count` climbing fast |
| `/monitoring/metrics/endpoints` | latency of device and API endpoints |
| `/monitoring/metrics/errors` | endpoints returning errors |
| `/monitoring/debug/goroutines` | steady growth (leaked streams or connections) |
| `/monitoring/debug/memory` | heap growth |

Turn the per-device request log off for large runs unless you are debugging specific
devices: it writes every request to disk.
