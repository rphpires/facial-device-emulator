# Facial Device Emulator - developer tasks.
# Run "make help" for the list of targets.

BINARY  := bin/facial-device-emulator
CONFIG  ?= configs/config.yaml
COMPOSE ?= docker compose
# Keep in sync with .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION := v2.15.0

.DEFAULT_GOAL := help
.PHONY: help run build test test-race vet lint fmt db-up db-down docker-up docker-down docker-logs packages clean

help: ## List the targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

run: ## Run the service from source (needs PostgreSQL, see db-up)
	go run ./cmd/emulator-service -config $(CONFIG)

build: ## Build the binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/emulator-service

test: ## Run the unit tests
	go test ./...

test-race: ## Run the unit tests with the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint in Docker, same version as CI (configuration in .golangci.yml)
	docker run --rm -v "$(CURDIR)":/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run ./...

fmt: ## Format the code
	gofmt -w cmd internal assets

db-up: ## Start PostgreSQL only, for "make run"
	$(COMPOSE) -f docker-compose.db-only.yml up -d --wait

db-down: ## Stop the PostgreSQL started by db-up
	$(COMPOSE) -f docker-compose.db-only.yml down

docker-up: ## Build and start the full stack (PostgreSQL + service)
	$(COMPOSE) up -d --build --wait

docker-down: ## Stop the full stack (add ARGS=-v to also drop the database volume)
	$(COMPOSE) down $(ARGS)

docker-logs: ## Follow the service logs
	$(COMPOSE) logs -f app

packages: ## Build the Docker, Windows and Linux distribution ZIPs
	packaging/build-packages.sh all

clean: ## Remove build output
	rm -rf bin packaging/.out
