Facial Device Emulator - Windows package (no Docker required)

WHAT TO DO
  1. Double-click install.bat (first time only, takes about a minute).
  2. Double-click start.bat.
  3. Open http://localhost:7070 in a browser.
  To stop: stop.bat

  Nothing else needs to be installed: a portable PostgreSQL is bundled
  (it listens on 127.0.0.1:5433).

PORTS
  Each emulated device listens on the port configured for it.
  There is no published-range limit in this package: the application runs
  directly on Windows, with no Docker in between.
  Windows Firewall may block the ports. If the access control system cannot
  connect, check that the device port range is allowed in Windows Firewall.

LOGS
  Application:  app\logs\trace.log (rotated: trace.log.1, trace.log.2, ...)
  Per device:   app\logs\devices\
  Database:     app\logs\postgres.log
  Installation: app\logs\install.log

Full documentation: docs/ in the source repository.
