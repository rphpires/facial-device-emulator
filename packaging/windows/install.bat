@echo off
setlocal
cd /d "%~dp0"
title Facial Device Emulator - Install

set "PGBIN=%~dp0app\postgres\bin"
set "PGDATA=%~dp0app\postgres\data"
set "PGPORT=5433"

if not exist app\logs mkdir app\logs
set "LOG=%~dp0app\logs\install.log"
echo. > "%LOG%"

REM Each step writes to its own temporary file (never reopened) so it does
REM not fight the antivirus over the same file. At the end everything is
REM gathered into app\logs\install.log in a single write.
set "T1=%TEMP%\fde_install_1.log"
set "T2=%TEMP%\fde_install_2.log"
set "T3=%TEMP%\fde_install_3.log"
set "T4=%TEMP%\fde_install_4.log"
set "SQLFILE=%TEMP%\fde_install_create.sql"
set "CHKUSER=%TEMP%\fde_install_chkuser.txt"
set "CHKDB=%TEMP%\fde_install_chkdb.txt"
del /f /q "%T1%" "%T2%" "%T3%" "%T4%" "%SQLFILE%" "%CHKUSER%" "%CHKDB%" >nul 2>&1

REM If PostgreSQL is already running (port 5433 answering) when this script
REM starts, leave it alone: do not start it again and do not stop it at the
REM end. Only stop what this script started.
set "PG_ALREADY_RUNNING=0"

echo ==============================================================
echo   Facial Device Emulator - Install (Windows)
echo ==============================================================
echo.

if not exist "%PGBIN%\postgres.exe" (
    echo [ERROR] Incomplete package: app\postgres\bin\postgres.exe is missing.
    echo         Download the package ZIP again.
    echo.
    pause
    exit /b 1
)

if exist "%PGDATA%\PG_VERSION" goto existing_db

if not exist "%PGDATA%" goto create_db
set "PGDATA_EMPTY=1"
for /f %%F in ('dir /a /b "%PGDATA%" 2^>nul') do set "PGDATA_EMPTY=0"
if "%PGDATA_EMPTY%"=="0" (
    echo [ERROR] The folder app\postgres\data exists but looks incomplete
    echo         ^(a previous install was probably interrupted^).
    echo         Delete app\postgres\data and run install.bat again.
    echo.
    pause
    exit /b 1
)

:create_db
echo [1/3] Creating the database cluster ...
set "PWFILE=%TEMP%\fde_pw.txt"
> "%PWFILE%" echo postgres
"%PGBIN%\initdb.exe" -D "%PGDATA%" -U postgres --pwfile="%PWFILE%" -E UTF8 --locale=C >"%T1%" 2>&1
set RC=%ERRORLEVEL%
del /f /q "%PWFILE%" >nul 2>&1
if not %RC%==0 (
    type "%T1%" > "%LOG%" 2>nul
    echo.
    echo [ERROR] Could not create the database cluster. See app\logs\install.log
    echo.
    pause
    exit /b 1
)

powershell -NoProfile -Command "(Get-Content '%PGDATA%\postgresql.conf') -replace '^#?port\s*=.*', 'port = %PGPORT%' | Set-Content '%PGDATA%\postgresql.conf'"
goto start_db

:existing_db
echo The database cluster already exists, checking that it is complete ...

"%PGBIN%\pg_isready.exe" -h 127.0.0.1 -p %PGPORT% >nul 2>&1
set "PGISREADY_RC=%ERRORLEVEL%"

REM pg_isready: 0 = accepting connections, 1 = up but not accepting
REM connections yet ^(e.g. recovering from a crash^), 2 = no response ^(not
REM running^). "if not errorlevel 1" would test ">=1" and confuse 1 with 2,
REM so the captured value is compared directly.
if "%PGISREADY_RC%"=="0" (
    REM Already running: do not start it again ^(pg_ctl start would fail on
    REM the lock even when healthy^) and do not stop it at the end.
    set "PG_ALREADY_RUNNING=1"
    goto check_user
)

if "%PGISREADY_RC%"=="1" (
    REM Starting but not accepting connections yet. Not ours: do not start
    REM it again ^(it would hit the lock^) and do not go on to the user and
    REM database check ^(psql would fail in a confusing way^).
    echo.
    echo [ERROR] The database is starting ^(not accepting connections yet^).
    echo         Wait a few seconds and run install.bat again.
    echo.
    pause
    exit /b 1
)

:start_db
echo [2/3] Starting the database ...
"%PGBIN%\pg_ctl.exe" -D "%PGDATA%" -l "%~dp0app\logs\postgres.log" -w start >"%T2%" 2>&1
if errorlevel 1 (
    type "%T1%" "%T2%" > "%LOG%" 2>nul
    echo.
    echo [ERROR] Could not start the database. See app\logs\postgres.log
    echo.
    pause
    exit /b 1
)

:check_user
REM Only report "already installed" after confirming the application user
REM and database really exist: the cluster may have been created by an
REM earlier attempt that failed right after, before creating them.
set PGPASSWORD=postgres
"%PGBIN%\psql.exe" -h 127.0.0.1 -p %PGPORT% -U postgres -d postgres -t -A -c "SELECT 1 FROM pg_roles WHERE rolname='emulator';" >"%CHKUSER%" 2>nul
"%PGBIN%\psql.exe" -h 127.0.0.1 -p %PGPORT% -U postgres -d postgres -t -A -c "SELECT 1 FROM pg_database WHERE datname='emulator_db';" >"%CHKDB%" 2>nul
set "HAS_USER="
set "HAS_DB="
set /p HAS_USER=<"%CHKUSER%"
set /p HAS_DB=<"%CHKDB%"

if "%HAS_USER%"=="1" if "%HAS_DB%"=="1" (
    if "%PG_ALREADY_RUNNING%"=="0" "%PGBIN%\pg_ctl.exe" -D "%PGDATA%" -w stop >"%T4%" 2>&1
    type "%T1%" "%T2%" "%T4%" > "%LOG%" 2>nul
    del /f /q "%T1%" "%T2%" "%T3%" "%T4%" "%SQLFILE%" "%CHKUSER%" "%CHKDB%" >nul 2>&1
    set PGPASSWORD=
    echo The database was already installed. Nothing to do.
    echo.
    echo [OK] Installed. Run start.bat
    echo.
    pause
    exit /b 0
)

echo [3/3] Creating the application user and database ...
(
echo DO $do$
echo BEGIN
echo    IF NOT EXISTS ^(SELECT FROM pg_catalog.pg_roles WHERE rolname = 'emulator'^) THEN
echo       CREATE ROLE emulator LOGIN PASSWORD 'emulator123';
echo    END IF;
echo END
echo $do$;
echo.
echo SELECT 'CREATE DATABASE emulator_db OWNER emulator'
echo WHERE NOT EXISTS ^(SELECT FROM pg_database WHERE datname = 'emulator_db'^)
echo \gexec
) > "%SQLFILE%"

"%PGBIN%\psql.exe" -h 127.0.0.1 -p %PGPORT% -U postgres -d postgres -v ON_ERROR_STOP=1 -f "%SQLFILE%" >"%T3%" 2>&1
set RC=%ERRORLEVEL%
set PGPASSWORD=

if "%PG_ALREADY_RUNNING%"=="0" "%PGBIN%\pg_ctl.exe" -D "%PGDATA%" -w stop >"%T4%" 2>&1

type "%T1%" "%T2%" "%T3%" "%T4%" > "%LOG%" 2>nul
del /f /q "%T1%" "%T2%" "%T3%" "%T4%" "%SQLFILE%" "%CHKUSER%" "%CHKDB%" >nul 2>&1

if not %RC%==0 (
    echo.
    echo [ERROR] Could not create the application user. See app\logs\install.log
    echo.
    pause
    exit /b 1
)

echo.
echo [OK] Installed. Run start.bat
echo.
pause
