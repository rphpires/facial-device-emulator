# REST API reference

Base URL: `http://<host>:7070` (the `server.port` setting). Requests and responses are
JSON unless stated otherwise. There is no authentication: the service is a test tool and
should run on a trusted network. CORS is open (`Access-Control-Allow-Origin: *`).

All routes are registered in `internal/handlers/handlers.go`. The examples below were
produced by a running instance.

- [Device object](#device-object)
- [Fleet management](#fleet-management): list, create, range, update, delete, batch, start/stop all
- [Single device](#single-device): get, start, stop, mode, log, users, settings
- [Fleet status](#fleet-status): status, pool stats, reachability
- [Event stream](#event-stream)
- [Monitoring](#monitoring)
- [Device protocol endpoints](#device-protocol-endpoints)
- [Errors](#errors)

## Device object

```json
{
  "id": 21,
  "name": "dahua-4100",
  "ip_address": "127.0.0.1",
  "port": 4100,
  "model": "Dahua",
  "enabled": 1,
  "type": 1,
  "status": "running",
  "event_interval": 10,
  "total_users": 0,
  "log_enabled": 0
}
```

| Field | Type | Notes |
|---|---|---|
| `model` | string | `Hikvision` or `Dahua` |
| `enabled`, `log_enabled` | int | `1` or `0` |
| `type` | int | Derived from the model (`1` Dahua, `2` Hikvision) |
| `status` | string | `running` or `stopped` |
| `total_users` | int | Users enrolled on the device, updated by the watchdog every 10 s |

## Fleet management

### GET /api/emulators

Lists every device. An empty fleet returns an empty array.

```json
{"emulators": [ { "id": 1, "name": "hik-4000", "...": "..." } ], "count": 31}
```

### POST /api/emulators

Creates one device.

| Field | Required | Default | Notes |
|---|---|---|---|
| `name` | yes | | |
| `model` | yes | | `Hikvision` or `Dahua` |
| `port` | yes | | 1-65535, not used by another device nor by the service |
| `ip_address` | no | `127.0.0.1` | Reported by the device; the listener binds to all interfaces |
| `event_interval` | no | `10` | Seconds; must not be negative |
| `enabled` | no | `true` | |
| `auto_start` | no | `false` | Start the device right after creating it |

```bash
curl -X POST http://localhost:7070/api/emulators -H 'Content-Type: application/json' \
  -d '{"name":"lobby-1","model":"Hikvision","port":4500,"auto_start":true}'
```

`201 Created`:

```json
{"emulator":{"id":31,"name":"lobby-1","ip_address":"127.0.0.1","port":4500,"model":"Hikvision","enabled":1,"type":2,"status":"running","event_interval":10,"total_users":0,"log_enabled":0},"started":true}
```

If the device was created but failed to start, the answer is still `201`, with
`"start_error": "<reason>"` instead of `"started": true`.

### POST /api/emulators/range

Creates one device per port in `[port_start, port_end]`, named `<name_prefix>-<port>`.
All devices are created in one transaction: if any port conflicts, none is created.

| Field | Required | Default | Notes |
|---|---|---|---|
| `name_prefix` | yes | | |
| `model` | yes | | `Hikvision` or `Dahua` |
| `port_start`, `port_end` | yes | | At most 1000 ports (`MaxRangeSize`) |
| `ip_address` | no | `127.0.0.1` | |
| `event_interval` | no | `10` | |
| `enabled` | no | `true` | |
| `auto_start` | no | `false` | Starts the new devices, 32 at a time |

```bash
curl -X POST http://localhost:7070/api/emulators/range -H 'Content-Type: application/json' \
  -d '{"name_prefix":"hik","model":"Hikvision","port_start":4000,"port_end":4019}'
```

`201 Created`:

```json
{"count":20,"created":[{"id":1,"name":"hik-4000","port":4000},{"id":2,"name":"hik-4001","port":4001}, "..."]}
```

With `auto_start`, the response also has `"started": <n>` and, when some failed,
`"start_errors": [{"id": 7, "error": "..."}]`.

### PUT /api/emulators/:id

Replaces the editable fields of a **stopped** device. Same body as `POST /api/emulators`
(`auto_start` is ignored). A running device answers `409`.

```bash
curl -X PUT http://localhost:7070/api/emulators/31 -H 'Content-Type: application/json' \
  -d '{"name":"lobby-main","model":"Hikvision","port":4500,"event_interval":30}'
```

```json
{"emulator":{"id":31,"name":"lobby-main","ip_address":"127.0.0.1","port":4500,"model":"Hikvision","enabled":1,"type":2,"status":"stopped","event_interval":30,"total_users":0,"log_enabled":0}}
```

### DELETE /api/emulators/:id

Stops the device if needed and deletes it together with everything it stored (users,
cards, faces, fingerprints, settings).

```json
{"removed": 31}
```

### POST /api/emulators/batch

Applies one action to a list of device IDs.

```json
{"ids": [2, 4, 6, 8], "action": "stop"}
```

| Action | Effect | Counted as skipped when |
|---|---|---|
| `start` | Starts the device (fails if disabled) | already running |
| `stop` | Stops the device | not running |
| `enable` | Enables the device | already enabled |
| `disable` | Stops it if running, then disables it | already disabled |
| `delete` | Same as `DELETE /api/emulators/:id` | |
| `enable_log`, `disable_log` | Turns the request log on or off | |

Rules:

- IDs must be positive; duplicates are ignored; at most 1000 IDs.
- Up to 32 items run concurrently; the whole request has a 5-minute budget. Items not
  reached in time fail with `"not attempted: batch timed out"`.
- A valid request always answers `200`, even when some items failed. Only a malformed
  request (unknown action, empty or too many IDs, invalid JSON) answers `400`.
- `succeeded` includes the skipped items. `results` are in the order of the request.

```json
{
  "action": "stop",
  "requested": 7,
  "succeeded": 7,
  "failed": 0,
  "skipped": 0,
  "results": [{"id": 2, "ok": true}, {"id": 4, "ok": true}]
}
```

A skipped item and a failed item look like this:

```json
{"id": 2, "ok": true, "skipped": "not running"}
{"id": 99, "ok": false, "error": "device 99 not found"}
```

### POST /api/emulators/start, POST /api/emulators/stop

Start every enabled, stopped device / stop every running device.

- Without parameters the work runs in the background and the call answers
  `202 {"status": "starting"}` (or `"stopping"`). Progress is visible on the event stream
  and on `GET /api/status`.
- With `?wait=true` the call blocks until done and answers `200 {"status": "started"}`
  (or `"stopped"`). If some devices failed to start after 3 attempts, it answers `500`
  with `{"error": "failed to start N/M devices"}`.

"Start all" runs 20 starts concurrently and pauses the watchdog until 15 seconds after
the ramp. The server write timeout is 5 minutes; for very large fleets prefer the
background form and watch `/api/status`.

## Single device

### GET /api/devices, GET /api/devices/:id

`GET /api/devices` returns `{"devices": [...], "count": N}`. `GET /api/devices/:id`
returns one [device object](#device-object), `404` when the ID does not exist, or `500`
when the database cannot be read (a timeout is not reported as "not found").

### POST /api/devices/:id/start, POST /api/devices/:id/stop

```json
{"message": "Device started"}
```

Failures answer `{"error": "<reason>"}` with:

| Status | When |
|---|---|
| `404` | The device ID does not exist |
| `409` | Start: already running or starting, disabled, or the port is bound by another process. Stop: not running |
| `500` | Database failure, or a start that did not finish within 10 s |

### GET /api/devices/:id/mode, POST /api/devices/:id/mode

```bash
curl -X POST http://localhost:7070/api/devices/21/mode -H 'Content-Type: application/json' \
  -d '{"mode":"online"}'
```

```json
{"mode": "online"}
```

`mode` is `online` or `standalone`; anything else answers `400`. An unknown device ID
answers `404` (no setting is written for it). A device with no stored mode reports
`standalone`. The value is stored as the `LocalAuthentication` setting
(`0` online, `1` standalone), the same setting the access control system writes through
the device protocol.

### PUT /api/devices/:id/settings

Turns the per-device request log on or off. Applies immediately, running or not.
Turning it off deletes the device's log files. An unknown device ID answers `404`.

```json
{"log_enabled": true}
```

```json
{"message": "Settings updated"}
```

### GET /api/devices/:id/log, GET /api/devices/:id/log.zip

`/log` streams the current log file as `text/plain` (404 when logging is off or nothing
was logged yet). `/log.zip` downloads every rotated file as
`device-<id>-logs-<timestamp>.zip`. Sample:

```
# device 1 "hik-4000" Hikvision port 4000 - opened 2026-11-14 03:22:29
2026-11-14 03:22:29.054 LOG enabled
2026-11-14 03:23:09.544 CONN 172.25.0.1:35434 opened
2026-11-14 03:23:09.544 IN 172.25.0.1:35434 GET /status -> 200 0ms (0B/255B)
```

### GET /api/devices/:id/users

Users enrolled on the device, in one format for both models. Query parameters: `page`
(default 1), `per_page` (default 10, max 100), `search` (name or ID).

```json
{
  "model": "Hikvision",
  "page": 1,
  "per_page": 2,
  "total": 12,
  "users": [
    {"id": "1001", "name": "Test User", "card_no": "", "has_face": false, "valid_to": "2030-12-31T23:59:59Z"},
    {"id": "1002", "name": "User 2", "card_no": "", "has_face": false, "valid_to": "2030-12-31T23:59:59Z"}
  ]
}
```

### GET /api/devices/:id/settings

The key/value settings stored for the device, as written by the access control system
(and by the mode endpoint).

```json
{"settings": [{"cfg_id": "LocalAuthentication", "value": "0"}]}
```

## Fleet status

### GET /api/status

```json
{"total_devices":31,"running_devices":22,"stopped_devices":7,"disabled_devices":2,"timestamp":"2026-11-14T03:21:55.369259863Z"}
```

A disabled device counts as disabled whatever its stored status.

### GET /api/pool-stats

Statistics of the two database connections (see
[architecture.md](architecture.md#database-layer)).

```json
{
  "service_db":  {"acquired_conns":0,"idle_conns":11,"total_conns":11,"constructing_conns":0,"min_conns":5,"max_conns":135,"peak_connections":7,"avg_query_duration_ms":0,"empty_acquire_count":0},
  "emulator_db": {"acquired_conns":0,"idle_conns":8,"total_conns":8,"constructing_conns":0,"min_conns":5,"max_conns":135,"peak_connections":7,"avg_query_duration_ms":1,"empty_acquire_count":0},
  "timestamp": "2026-11-14T03:23:09.494684684Z"
}
```

### GET /api/reachability

Whether each device port can be reached from outside in the current environment.

```json
{
  "environment": {
    "kind": "docker",
    "published_ranges": [{"Start": 4000, "End": 4999}],
    "ranges_known": true,
    "host_network": false,
    "wsl_mirrored": false,
    "max_open_files": 524288
  },
  "devices": [
    {"device_id": 1, "port": 4000, "status": "ok"},
    {"device_id": 40, "port": 5000, "status": "unreachable", "reason": "port 5000 is not published in this Docker environment"}
  ],
  "unreachable": 1,
  "unknown": 0,
  "not_started": 0
}
```

`kind` is `linux`, `wsl`, `docker` or `windows`. Device `status` is `ok`, `unreachable`,
`unknown` (cannot tell, for example Docker without `PUBLISHED_PORT_RANGE`) or
`not_started` (native environment, device never started). Rules are described in
[ports-and-networking.md](ports-and-networking.md#reachability-check).

## Event stream

`GET /events` is a Server-Sent Events stream used by the console. It is usable by any
client that wants live fleet state.

```
retry: 3000

event: snapshot
data: {"counts":{"total":31,"running":22,"stopped":7,"disabled":2},"devices":[{"id":1,"name":"hik-4000","model":"Hikvision","ip_address":"127.0.0.1","port":4000,"status":"running","enabled":1,"log_enabled":0,"interval":10,"total_users":0,"mode":"standalone"}, ...]}

event: delta
data: {"counts":{...},"devices":[ ...changed devices... ],"removed":[31]}
```

- `snapshot` is sent on connect; `delta` frames follow with only the changed devices and
  the IDs removed since the last frame.
- Changes within 150 ms are coalesced into one frame.
- A comment line is sent every 20 seconds as keepalive.
- Device `status` in the stream is `disabled` for disabled devices; `last_error` is
  present when the last start failed.

## Monitoring

| Path | Response |
|---|---|
| `GET /monitoring/health` | Runs every checker. `200` when healthy or degraded, `503` when unhealthy |
| `GET /monitoring/health/quick` | Same shape, served from a short-lived cache |
| `GET /monitoring/metrics` | `{database, emulators, endpoints, ...}` counters |
| `GET /monitoring/metrics/endpoints` | `{endpoints: {"/path": {total_requests, total_4xx, total_5xx, avg_duration_ms, last_error, ...}}}` |
| `GET /monitoring/metrics/errors?limit=10` | `{top_errors: [...], limit, timestamp}` |
| `GET /monitoring/metrics/database` | Same as `/api/pool-stats` |
| `GET /monitoring/debug/goroutines` | `{"goroutines": 62, "timestamp": "..."}` |
| `GET /monitoring/debug/memory` | `{"alloc_mb", "heap_alloc_mb", "sys_mb", "num_gc", "gc_pause_ms", ...}` |

Health response (abridged):

```json
{
  "status": "healthy",
  "components": {
    "emulators":   {"name":"emulators","status":"healthy","message":"0/0 emulators running","metadata":{"running":0,"stopped":0,"errors":0,"total_devices":0}},
    "service_db":  {"name":"service_db","metadata":{"max_conns":45,"idle_conns":4,"acquired_conns":1}},
    "emulator_db": {"name":"emulator_db","metadata":{"max_conns":45,"idle_conns":5,"acquired_conns":0}}
  },
  "metrics": {"goroutines":10,"memory_alloc_mb":2,"total_checks":2,"total_failures":0}
}
```

## Device protocol endpoints

These are served by each emulated device on its own port, not by the management API.
They are what the access control system under test calls.

**Hikvision (ISAPI)**

| Method | Path |
|---|---|
| GET, PUT | `/ISAPI/AccessControl/AcsCfg` |
| PUT | `/ISAPI/AccessControl/AcsEvent/StorageCfg` |
| POST | `/ISAPI/AccessControl/AcsEvent` (event history search) |
| PUT | `/ISAPI/AccessControl/Door/param/1` |
| PUT | `/ISAPI/AccessControl/RemoteControl/door/:output_id` |
| PUT | `/ISAPI/AccessControl/remoteCheck` |
| GET | `/ISAPI/AccessControl/capabilities` |
| GET | `/ISAPI/AccessControl/UserInfo/Count` |
| POST | `/ISAPI/AccessControl/UserInfo/Search`, `/UserInfo/Record` |
| PUT | `/ISAPI/AccessControl/UserInfo/Modify` |
| GET | `/ISAPI/AccessControl/UserInfoDetail/DeleteProcess` |
| PUT | `/ISAPI/AccessControl/UserInfoDetail/Delete` |
| GET | `/ISAPI/AccessControl/CardInfo/Count` |
| POST | `/ISAPI/AccessControl/CardInfo/Search`, `/CardInfo/Record` |
| PUT | `/ISAPI/AccessControl/CardInfo/Delete` |
| POST | `/ISAPI/AccessControl/FingerPrint/SetUp`, `/FingerPrintUploadAll`, `/CaptureFingerPrint` |
| PUT | `/ISAPI/AccessControl/FingerPrint/Delete` |
| GET | `/ISAPI/Intelligent/FDLib/Count`, `/FDLib/LOCALS/pic/enrlFace/:user_id` |
| POST | `/ISAPI/Intelligent/FDLib/FDSearch`, `/FDLib/FaceDataRecord` |
| PUT | `/ISAPI/Intelligent/FDLib/FDSearch/Delete`, `/FDLib/FDSetUp` |
| GET, PUT | `/ISAPI/System/time` |
| GET | `/ISAPI/System/deviceInfo` |
| PUT | `/ISAPI/System/IO/outputs/:output_id/trigger` |
| GET, PUT | `/ISAPI/Event/notification/httpHosts` |
| GET | `/ISAPI/Event/notification/alertStream` (long-lived multipart stream) |
| GET | `/emulator/get-status`, `/status`, `/health` (emulator-specific) |

**Dahua (CGI)**

| Method | Path | Actions |
|---|---|---|
| GET | `/cgi-bin/global.cgi` | `getCurrentTime`, `setCurrentTime`, `setConfig` |
| GET | `/cgi-bin/magicBox.cgi` | `getSoftwareVersion` |
| GET | `/cgi-bin/configManager.cgi` | `getConfig`, `setConfig` |
| GET | `/cgi-bin/accessControl.cgi` | `openDoor`, `closeDoor`, `captureFingerprint` |
| GET | `/cgi-bin/FaceInfoManager.cgi` | `startFind`, `doFind`, `stopFind`, `remove` |
| POST | `/cgi-bin/FaceInfoManager.cgi` | `add`, `update` |
| GET | `/cgi-bin/recordFinder.cgi` | `find`, `doSeekFind`, `getQuerySize`, `startFind`, `stopFind` (`AccessControlCard`, `AccessControlCardRec`) |
| GET, POST | `/cgi-bin/recordUpdater.cgi` | `insert`, `remove` |
| GET | `/cgi-bin/snapManager.cgi` | event stream with text + JPEG parts (any query) |
| GET | `/cgi-bin/eventManager.cgi` | `attach` stream (text parts) |
| GET | `/cgi-bin/AccessFace.cgi` | `get`, `list` |
| POST | `/cgi-bin/AccessFace.cgi` | `insertMulti`, `updateMulti`, `list` |
| GET | `/cgi-bin/AccessUser.cgi` | `remove` (answers 501, as the real device does) |
| GET | `/emulator/get-status` | emulator-specific status |

## Errors

Errors have the shape `{"error": "<message>"}`.

| Status | When |
|---|---|
| `400` | Invalid JSON, invalid payload (missing name, unknown model, bad port, range over 1000 ports), port conflict, unknown batch action or mode |
| `404` | Device not found |
| `409` | Editing a running device; starting a device that is running, starting, disabled or whose port is taken; stopping a device that is not running |
| `500` | Database failure or other unexpected error (details in the trace log; the body stays generic, except for start/stop, which report the reason) |

A port conflict also lists the ports:

```json
{"error": "ports already used by other emulators: [4000]", "conflicts": [4000]}
{"error": "ports reserved by the service itself: [7070]", "reserved": [7070]}
```
