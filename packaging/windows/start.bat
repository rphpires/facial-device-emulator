@echo off
setlocal
cd /d "%~dp0app"
title Facial Device Emulator

set "PGBIN=%~dp0app\postgres\bin"
set "PGDATA=%~dp0app\postgres\data"

if not exist logs mkdir logs

echo ==============================================================
echo   Facial Device Emulator - Start
echo ==============================================================
echo.

if not exist "%PGDATA%\PG_VERSION" (
    echo [ERROR] The database is not installed yet. Run install.bat first.
    echo.
    pause
    exit /b 1
)

netstat -ano | findstr /r /c:":7070 .*LISTENING" >nul 2>&1
if not errorlevel 1 (
    echo [ERROR] Port 7070 is already in use.
    echo         The emulator may already be running: open http://localhost:7070
    echo         If it is not, run stop.bat and try again.
    echo.
    pause
    exit /b 1
)

echo [1/3] Starting the database ...
"%PGBIN%\pg_ctl.exe" -D "%PGDATA%" -l "logs\postgres.log" -w start >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Could not start the database. See app\logs\postgres.log
    echo.
    pause
    exit /b 1
)

echo [2/3] Starting the application ...
start "Facial Device Emulator" /min facial-device-emulator.exe -config configs\config.yaml

echo [3/3] Waiting for the application to answer ...
set /a attempts=0
:wait_app
set /a attempts+=1
curl -sf http://localhost:7070/monitoring/health/quick >nul 2>&1
if not errorlevel 1 goto ready
if %attempts% geq 60 (
    echo.
    echo [ERROR] The application did not answer within 60 seconds.
    echo         See app\logs\trace.log
    echo.
    pause
    exit /b 1
)
timeout /t 1 /nobreak >nul
goto wait_app

:ready
echo.
echo [OK] Running at http://localhost:7070
echo.
echo      To stop: stop.bat
echo.
pause
