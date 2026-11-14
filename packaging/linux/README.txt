Facial Device Emulator - Linux package (also works on WSL2)

WHAT TO DO
  1. sudo ./install.sh    (first time only)
  2. ./start.sh
  3. Open http://localhost:7070 in a browser.
  To stop: ./stop.sh

  On WSL2 the same commands apply: the installer detects the environment
  and uses "service" instead of "systemctl".

  If ./install.sh or ./start.sh will not run for lack of execute
  permission, use: sudo bash install.sh   /   bash start.sh

PORTS
  Each emulated device listens on the port configured for it.
  install.sh asks for the port range and opens it in the firewall
  (ufw or firewalld, when active).
  On WSL2, other machines reach the emulated devices only with mirrored
  networking enabled. install.sh warns about it and shows how to enable it.

LOGS
  Application: app/logs/trace.log (rotated: trace.log.1, trace.log.2, ...)
  Per device:  app/logs/devices/
  Raw output:  app/logs/app.out
  Database:    /var/log/postgresql/

Full documentation: docs/ in the source repository.
