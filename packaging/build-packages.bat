@echo off
setlocal enabledelayedexpansion
cd /d "%~dp0.."

REM Builds the distribution ZIPs into packaging\.out (same output as
REM build-packages.sh on Linux).
REM Usage: packaging\build-packages.bat [docker^|windows^|linux^|all]

set "TARGET=%~1"
if "%TARGET%"=="" set "TARGET=all"

set "OUT=packaging\.out"
set "NAME=facial-device-emulator"
set "IMAGE=%NAME%:1.0"
if not exist "%OUT%" mkdir "%OUT%"

where go >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Go not found. Install it from https://go.dev/dl/
    exit /b 1
)

if /i "%TARGET%"=="docker"  goto build_docker
if /i "%TARGET%"=="windows" goto build_windows
if /i "%TARGET%"=="linux"   goto build_linux
if /i "%TARGET%"=="all"     goto build_docker
echo [ERROR] Invalid target: %TARGET%
echo Usage: build-packages.bat [docker^|windows^|linux^|all]
exit /b 1

REM ==================== DOCKER ====================
:build_docker
echo.
echo [docker] Building the image ...
docker build -t %IMAGE% .
if errorlevel 1 (
    echo [ERROR] docker build failed.
    exit /b 1
)

set "STAGE=%OUT%\docker"
if exist "%STAGE%" rmdir /s /q "%STAGE%"
mkdir "%STAGE%\app\logs"

echo [docker] Exporting the image ...
docker save -o "%STAGE%\app\%NAME%-image.tar" %IMAGE%
if errorlevel 1 (
    echo [ERROR] docker save failed.
    exit /b 1
)

copy /Y packaging\docker\docker-compose.yml       "%STAGE%\app\" >nul
copy /Y packaging\docker\docker-compose.linux.yml "%STAGE%\app\" >nul
copy /Y packaging\docker\install.bat "%STAGE%\" >nul
copy /Y packaging\docker\start.bat   "%STAGE%\" >nul
copy /Y packaging\docker\stop.bat    "%STAGE%\" >nul
copy /Y packaging\docker\install.sh  "%STAGE%\" >nul
copy /Y packaging\docker\start.sh    "%STAGE%\" >nul
copy /Y packaging\docker\stop.sh     "%STAGE%\" >nul
copy /Y packaging\docker\README.txt  "%STAGE%\" >nul

if exist "%OUT%\%NAME%-docker.zip" del "%OUT%\%NAME%-docker.zip"
powershell -NoProfile -Command "Compress-Archive -Path '%STAGE%\*' -DestinationPath '%OUT%\%NAME%-docker.zip' -Force"
if errorlevel 1 (
    echo [ERROR] Could not create the ZIP.
    exit /b 1
)
echo [docker] OK: %OUT%\%NAME%-docker.zip
if /i not "%TARGET%"=="all" goto done

REM ==================== WINDOWS ====================
:build_windows
set "PGCACHE=.build-cache\postgres-portable"
if not exist "%PGCACHE%\bin\postgres.exe" (
    echo [windows] Downloading portable PostgreSQL ^(once^) ...
    if not exist .build-cache mkdir .build-cache
    powershell -NoProfile -Command "$u='https://get.enterprisedb.com/postgresql/postgresql-15.8-1-windows-x64-binaries.zip'; Invoke-WebRequest -Uri $u -OutFile '.build-cache\pg.zip'; Expand-Archive -Path '.build-cache\pg.zip' -DestinationPath '.build-cache\pg' -Force; Move-Item '.build-cache\pg\pgsql' '%PGCACHE%' -Force; Remove-Item '.build-cache\pg.zip','.build-cache\pg' -Recurse -Force"
    if not exist "%PGCACHE%\bin\postgres.exe" (
        echo [ERROR] Could not download portable PostgreSQL.
        exit /b 1
    )
)

set "STAGE=%OUT%\windows"
if exist "%STAGE%" rmdir /s /q "%STAGE%"
mkdir "%STAGE%\app\logs"
mkdir "%STAGE%\app\configs"

echo [windows] Compiling ...
set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags="-s -w" -o "%STAGE%\app\%NAME%.exe" .\cmd\emulator-service
if errorlevel 1 (
    echo [ERROR] Build failed.
    exit /b 1
)

echo [windows] Copying portable PostgreSQL ...
xcopy /E /I /Y /Q "%PGCACHE%\bin"   "%STAGE%\app\postgres\bin"   >nul
xcopy /E /I /Y /Q "%PGCACHE%\lib"   "%STAGE%\app\postgres\lib"   >nul
xcopy /E /I /Y /Q "%PGCACHE%\share" "%STAGE%\app\postgres\share" >nul

copy /Y packaging\windows\config.yaml "%STAGE%\app\configs\config.yaml" >nul
copy /Y packaging\windows\install.bat "%STAGE%\" >nul
copy /Y packaging\windows\start.bat   "%STAGE%\" >nul
copy /Y packaging\windows\stop.bat    "%STAGE%\" >nul
copy /Y packaging\windows\README.txt  "%STAGE%\" >nul

if exist "%OUT%\%NAME%-windows.zip" del "%OUT%\%NAME%-windows.zip"
powershell -NoProfile -Command "Compress-Archive -Path '%STAGE%\*' -DestinationPath '%OUT%\%NAME%-windows.zip' -Force"
if errorlevel 1 (
    echo [ERROR] Could not create the ZIP.
    exit /b 1
)
echo [windows] OK: %OUT%\%NAME%-windows.zip
if /i not "%TARGET%"=="all" goto done

REM ==================== LINUX ====================
:build_linux
set "STAGE=%OUT%\linux"
if exist "%STAGE%" rmdir /s /q "%STAGE%"
mkdir "%STAGE%\app\logs"
mkdir "%STAGE%\app\configs"

echo [linux] Compiling ...
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64
go build -trimpath -ldflags="-s -w" -o "%STAGE%\app\%NAME%" .\cmd\emulator-service
if errorlevel 1 (
    echo [ERROR] Build failed.
    exit /b 1
)

copy /Y packaging\linux\config.yaml "%STAGE%\app\configs\config.yaml" >nul
copy /Y packaging\linux\install.sh  "%STAGE%\" >nul
copy /Y packaging\linux\start.sh    "%STAGE%\" >nul
copy /Y packaging\linux\stop.sh     "%STAGE%\" >nul
copy /Y packaging\linux\README.txt  "%STAGE%\" >nul

if exist "%OUT%\%NAME%-linux.zip" del "%OUT%\%NAME%-linux.zip"
powershell -NoProfile -Command "Compress-Archive -Path '%STAGE%\*' -DestinationPath '%OUT%\%NAME%-linux.zip' -Force"
if errorlevel 1 (
    echo [ERROR] Could not create the ZIP.
    exit /b 1
)
echo [linux] OK: %OUT%\%NAME%-linux.zip

:done
echo.
echo Packages written to %OUT%\
dir /b "%OUT%\*.zip"
exit /b 0
