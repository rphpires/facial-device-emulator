@echo off
setlocal
cd /d "%~dp0"
title Facial Device Emulator

if not exist app\logs mkdir app\logs
set "LOG=app\logs\install.log"

REM The command writes to its own temporary file (never reopened) so it does
REM not fight the antivirus over the same file; it is copied into
REM app\logs\install.log in a single write at the end.
set "T1=%TEMP%\fde_docker_start_1.log"
del /f /q "%T1%" >nul 2>&1

echo ==============================================================
echo   Facial Device Emulator - Start
echo ==============================================================
echo.

docker info >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Docker is not running. Open Docker Desktop and try again.
    echo.
    pause
    exit /b 1
)

docker image inspect facial-device-emulator:1.0 >nul 2>&1
if errorlevel 1 (
    echo [ERROR] The application is not installed yet. Run install.bat first.
    echo.
    pause
    exit /b 1
)

echo [1/2] Starting the services ...
docker compose -f app\docker-compose.yml up -d >"%T1%" 2>&1
if errorlevel 1 (
    type "%T1%" > "%LOG%" 2>nul
    echo [ERROR] Could not start the services. See app\logs\install.log
    echo.
    pause
    exit /b 1
)
type "%T1%" > "%LOG%" 2>nul
del /f /q "%T1%" >nul 2>&1

echo [2/2] Waiting for the application to answer ...
set /a attempts=0
:wait_app
set /a attempts+=1
curl -sf http://localhost:7070/monitoring/health/quick >nul 2>&1
if not errorlevel 1 goto ready
if %attempts% geq 60 (
    echo.
    echo [ERROR] The application did not answer within 60 seconds.
    echo         See the log with: docker compose -f app\docker-compose.yml logs app
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
