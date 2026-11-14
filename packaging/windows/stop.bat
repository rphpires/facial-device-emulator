@echo off
setlocal
cd /d "%~dp0"
title Facial Device Emulator - Stop

set "PGBIN=%~dp0app\postgres\bin"
set "PGDATA=%~dp0app\postgres\data"

echo Stopping the application ...
taskkill /IM facial-device-emulator.exe /F >nul 2>&1

echo Stopping the database ...
"%PGBIN%\pg_ctl.exe" -D "%PGDATA%" -w stop >nul 2>&1

tasklist /NH /FI "IMAGENAME eq postgres.exe" 2>nul | findstr /I "postgres.exe" >nul
if not errorlevel 1 (
    echo.
    echo [ERROR] The database did not stop completely: postgres.exe processes are still running.
    echo         Close them in Task Manager and run stop.bat again.
    echo.
    pause
    exit /b 1
)

echo.
echo [OK] Stopped. Data is kept.
echo.
pause
