@echo off
setlocal
cd /d "%~dp0"
title Facial Device Emulator - Install

if not exist app\logs mkdir app\logs
set "LOG=app\logs\install.log"

REM Each command writes to its own temporary file (never reopened) so it does
REM not fight the antivirus over the same file. At the end everything is
REM gathered into app\logs\install.log in a single write.
set "T1=%TEMP%\fde_docker_install_1.log"
set "T2=%TEMP%\fde_docker_install_2.log"
set "T3=%TEMP%\fde_docker_install_3.log"
del /f /q "%T1%" "%T2%" "%T3%" >nul 2>&1

echo ==============================================================
echo   Facial Device Emulator - Install (Docker)
echo ==============================================================
echo.

echo [1/3] Checking Docker ...
docker info >"%T1%" 2>&1
if errorlevel 1 (
    type "%T1%" > "%LOG%" 2>nul
    echo.
    echo [ERROR] Docker is not running.
    echo         Open Docker Desktop, wait until it reports it is running,
    echo         and run install.bat again.
    echo.
    pause
    exit /b 1
)
echo       Docker OK.

echo [2/3] Loading the application image ...
docker load -i app\facial-device-emulator-image.tar >"%T2%" 2>&1
if errorlevel 1 (
    type "%T1%" "%T2%" > "%LOG%" 2>nul
    echo.
    echo [ERROR] Could not load the image. See app\logs\install.log
    echo.
    pause
    exit /b 1
)
echo       Image loaded.

echo [3/3] Preparing the database ...
docker compose -f app\docker-compose.yml up -d postgres >"%T3%" 2>&1
if errorlevel 1 (
    type "%T1%" "%T2%" "%T3%" > "%LOG%" 2>nul
    echo.
    echo [ERROR] Could not prepare the database. See app\logs\install.log
    echo.
    pause
    exit /b 1
)

REM "docker compose up -d" only confirms the container started, not that the
REM database accepts connections. --wait would solve it but needs Docker
REM Compose 2.17+; the package supports 2.0, so poll the healthcheck instead.
set "PGSTATUS="
set /a attempts=0
:wait_db
set /a attempts+=1
for /f %%s in ('docker inspect -f "{{.State.Health.Status}}" facial-device-emulator-db 2^>nul') do set "PGSTATUS=%%s"
if "%PGSTATUS%"=="healthy" goto db_ready
if %attempts% geq 120 (
    type "%T1%" "%T2%" "%T3%" > "%LOG%" 2>nul
    echo.
    echo [ERROR] The database did not become ready in time. See app\logs\install.log
    echo.
    pause
    exit /b 1
)
timeout /t 1 /nobreak >nul
goto wait_db

:db_ready
echo       Database ready.

type "%T1%" "%T2%" "%T3%" > "%LOG%" 2>nul
del /f /q "%T1%" "%T2%" "%T3%" >nul 2>&1

echo.
echo [OK] Installed. Run start.bat
echo.
pause
