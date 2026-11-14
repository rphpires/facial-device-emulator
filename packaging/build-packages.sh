#!/usr/bin/env bash
# Builds the distribution ZIPs into packaging/.out (same output as
# build-packages.bat on Windows).
# Usage: packaging/build-packages.sh [docker|windows|linux|all]
set -euo pipefail
cd "$(dirname "$0")/.."

TARGET="${1:-all}"
OUT="packaging/.out"
NAME="facial-device-emulator"
IMAGE="$NAME:1.0"
mkdir -p "$OUT"

fail() { echo "[ERROR] $*" >&2; exit 1; }

command -v go  >/dev/null || fail "Go not found. Install it from https://go.dev/dl/"
command -v zip >/dev/null || fail "zip not found. Install it with: sudo apt install zip"

# make_zip STAGE ZIP: zips the contents of STAGE (not the folder itself).
make_zip() {
    local stage="$1" dest
    dest="$(pwd)/$2"
    rm -f "$dest"
    (cd "$stage" && zip -qr "$dest" .)
}

build_docker() {
    echo
    echo "[docker] Building the image ..."
    docker build -t "$IMAGE" . || fail "docker build failed."

    local stage="$OUT/docker"
    rm -rf "$stage"
    mkdir -p "$stage/app/logs"

    echo "[docker] Exporting the image ..."
    docker save -o "$stage/app/$NAME-image.tar" "$IMAGE" || fail "docker save failed."

    cp packaging/docker/docker-compose.yml packaging/docker/docker-compose.linux.yml "$stage/app/"
    cp packaging/docker/{install.bat,start.bat,stop.bat,install.sh,start.sh,stop.sh,README.txt} "$stage/"
    chmod +x "$stage"/*.sh

    make_zip "$stage" "$OUT/$NAME-docker.zip" || fail "Could not create the ZIP."
    echo "[docker] OK: $OUT/$NAME-docker.zip"
}

build_windows() {
    local pgcache=".build-cache/postgres-portable"
    if [ ! -f "$pgcache/bin/postgres.exe" ]; then
        echo "[windows] Downloading portable PostgreSQL (once) ..."
        command -v curl  >/dev/null || fail "curl not found."
        command -v unzip >/dev/null || fail "unzip not found."
        mkdir -p .build-cache
        rm -rf .build-cache/pg "$pgcache"
        curl -fL -o .build-cache/pg.zip \
            https://get.enterprisedb.com/postgresql/postgresql-15.8-1-windows-x64-binaries.zip
        unzip -q .build-cache/pg.zip -d .build-cache/pg
        mv .build-cache/pg/pgsql "$pgcache"
        rm -rf .build-cache/pg.zip .build-cache/pg
        [ -f "$pgcache/bin/postgres.exe" ] || fail "Could not download portable PostgreSQL."
    fi

    local stage="$OUT/windows"
    rm -rf "$stage"
    mkdir -p "$stage/app/logs" "$stage/app/configs" "$stage/app/postgres"

    echo "[windows] Compiling ..."
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
        go build -trimpath -ldflags="-s -w" -o "$stage/app/$NAME.exe" ./cmd/emulator-service \
        || fail "Build failed."

    echo "[windows] Copying portable PostgreSQL ..."
    cp -r "$pgcache/bin" "$pgcache/lib" "$pgcache/share" "$stage/app/postgres/"

    cp packaging/windows/config.yaml "$stage/app/configs/config.yaml"
    cp packaging/windows/{install.bat,start.bat,stop.bat,README.txt} "$stage/"

    make_zip "$stage" "$OUT/$NAME-windows.zip" || fail "Could not create the ZIP."
    echo "[windows] OK: $OUT/$NAME-windows.zip"
}

build_linux() {
    local stage="$OUT/linux"
    rm -rf "$stage"
    mkdir -p "$stage/app/logs" "$stage/app/configs"

    echo "[linux] Compiling ..."
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
        go build -trimpath -ldflags="-s -w" -o "$stage/app/$NAME" ./cmd/emulator-service \
        || fail "Build failed."

    cp packaging/linux/config.yaml "$stage/app/configs/config.yaml"
    cp packaging/linux/{install.sh,start.sh,stop.sh,README.txt} "$stage/"
    chmod +x "$stage"/*.sh

    make_zip "$stage" "$OUT/$NAME-linux.zip" || fail "Could not create the ZIP."
    echo "[linux] OK: $OUT/$NAME-linux.zip"
}

case "$TARGET" in
    docker)  build_docker ;;
    windows) build_windows ;;
    linux)   build_linux ;;
    all)     build_docker; build_windows; build_linux ;;
    *) echo "[ERROR] Invalid target: $TARGET" >&2
       echo "Usage: build-packages.sh [docker|windows|linux|all]" >&2
       exit 1 ;;
esac

echo
echo "Packages written to $OUT/"
ls -1 "$OUT"/*.zip 2>/dev/null || true
