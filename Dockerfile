# Build stage
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Download modules first so they are cached across source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binary. Templates, static files and SQL migrations are embedded
# with go:embed, so only the binary and the config file are needed at runtime.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/facial-device-emulator ./cmd/emulator-service

# Runtime stage
FROM alpine:3.24

WORKDIR /app

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /out/facial-device-emulator .
COPY --from=builder /src/configs ./configs

# Optional service log file (log.file / LOG_FILE) and per-device logs.
RUN mkdir -p logs

# Web console and REST API. Emulated devices listen on their own ports,
# published by the compose file.
EXPOSE 7070

HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 \
    CMD wget -qO- http://127.0.0.1:${SERVER_PORT:-7070}/monitoring/health/quick >/dev/null || exit 1

CMD ["./facial-device-emulator", "-config", "configs/config.yaml"]
