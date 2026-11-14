Facial Device Emulator - Docker package

WHAT TO DO
  1. Start Docker Desktop (Windows/macOS) or the Docker Engine (Linux).
  2. Windows: double-click install.bat (first time only).
     Linux/macOS: ./install.sh
  3. Windows: double-click start.bat.   Linux/macOS: ./start.sh
  4. Open http://localhost:7070 in a browser.
  To stop: stop.bat or ./stop.sh

PORTS
  Each emulated device listens on the port configured for it.
  On Windows/macOS this package publishes ports 4000-4499. A device on a
  port outside that range starts, but is NOT reachable from outside the host.
  To widen the range, edit app/docker-compose.yml: the ports line and the
  PUBLISHED_PORT_RANGE variable must hold the SAME range.
  On native Linux there is no limit: the package uses host networking
  (app/docker-compose.linux.yml).

LOGS
  Application: app/logs/trace.log (rotated: trace.log.1, trace.log.2, ...)
  Per device:  app/logs/devices/
  Database:    docker compose -f app/docker-compose.yml logs postgres

Full documentation: docs/ in the source repository.
