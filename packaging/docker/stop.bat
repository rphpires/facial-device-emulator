@echo off
setlocal
cd /d "%~dp0"
title Facial Device Emulator - Stop

if not exist app\logs mkdir app\logs
set "LOG=app\logs\install.log"
set "T1=%TEMP%\fde_docker_stop_1.log"
del /f /q "%T1%" >nul 2>&1

echo Stopping the Facial Device Emulator ...
docker compose -f app\docker-compose.yml stop >"%T1%" 2>&1
if errorlevel 1 (
    type "%T1%" > "%LOG%" 2>nul
    echo.
    echo [ERROR] Could not stop the services. See app\logs\install.log
    echo.
    pause
    exit /b 1
)
type "%T1%" > "%LOG%" 2>nul
del /f /q "%T1%" >nul 2>&1

echo.
echo [OK] Stopped. Data is kept.
echo.
pause
