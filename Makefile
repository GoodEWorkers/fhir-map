.PHONY: build run test test-unit test-integration lint clean migrate-up migrate-down docker-up docker-down dev wait-for-postgres load-test stress-test load-test-down hooks setup tools verify

# Build variables
BINARY_NAME=fhir-map-server
BUILD_DIR=./bin
GO=go
GOFLAGS=-ldflags="-s -w"

# Database variables
DB_HOST?=localhost
DB_PORT?=5432
DB_USER?=fhir
DB_PASSWORD?=fhir
DB_NAME?=fhir
DB_SSL_MODE?=disable
DATABASE_URL=postgres://$(DB_USER):$(DB_PASSWORD)@$(DB_HOST):$(DB_PORT)/$(DB_NAME)?sslmode=$(DB_SSL_MODE)

# Migration tool
MIGRATE_BIN=$(shell go env GOPATH)/bin/migrate
ifeq ($(wildcard $(MIGRATE_BIN)),)
  MIGRATE_BIN=migrate
endif

# Build the application
build:
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/server

# Run the application
run: build
	$(BUILD_DIR)/$(BINARY_NAME)

# Run all tests
test:
	$(GO) test -v -race -count=1 ./...

# Run unit tests only (skip integration tests requiring Docker)
test-unit:
	$(GO) test -v -race -short -count=1 ./...

# Run integration tests only (matches CI: -tags integration compiles //go:build integration
# files; no -run filter so TestStructureDefinitionRepo_* tests are also exercised)
test-integration:
	$(GO) test -v -tags integration -race -count=1 ./...

# Run tests with coverage
test-coverage:
	$(GO) test -v -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

# Run benchmarks
bench:
	$(GO) test -bench=. -benchmem ./...

# Lint the code
lint:
	golangci-lint run ./...

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

# Database migrations
migrate-up:
	$(MIGRATE_BIN) -path internal/repository/postgres/migrations -database "$(DATABASE_URL)" up

migrate-down:
	$(MIGRATE_BIN) -path internal/repository/postgres/migrations -database "$(DATABASE_URL)" down

# Docker commands. `docker-up` only brings up postgres so local `make dev` is
# not blocked by image rebuilds; use `docker-up-all` to also bring the server
# container up.
docker-up:
	docker compose up -d postgres

docker-up-all:
	docker compose up -d --build

docker-down:
	docker compose down -v

# Block until Postgres accepts connections. Avoids the race where `make dev`
# tried to run migrations / boot the server before pg was actually ready.
# `pg_isready` is bundled in the postgres image; we shell into the container
# rather than installing it locally.
wait-for-postgres:
	@echo "Waiting for PostgreSQL to be ready..."
	@for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do \
		if docker compose exec -T postgres pg_isready -U $(DB_USER) >/dev/null 2>&1; then \
			echo "  ✓ postgres ready (attempt $$i)"; exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "  ✗ postgres did not become ready in 15s"; exit 1

# Development: start postgres, run migrations, boot the server.
dev: docker-up wait-for-postgres
	@make migrate-up
	@make run

# ─── Load & Stress Testing ────────────────────────────────────────────────────
# Requires Docker and docker compose v2.
#
# load-test  — ramp + sustain + spike; enforces SLO thresholds.
#              Server constrained to 1 vCPU / 1 GB RAM.
#
# stress-test — max-concurrency flood (200 VUs, 3 min); finds the breaking
#               point. Thresholds are relaxed; goal is saturation analysis.
#
# load-test-down — tear down the load-test stack (removes containers + volumes).

load-test:
	@echo "Building image and starting constrained stack (1 vCPU / 1 GB)..."
	docker compose \
		-f docker-compose.yml \
		-f docker-compose.loadtest.yml \
		--profile loadtest \
		up --build --abort-on-container-exit --exit-code-from k6
	@echo "k6 results written to loadtest/results.json"

stress-test:
	@echo "Running stress test (200 VUs, no ramp, 3 min)..."
	docker compose \
		-f docker-compose.yml \
		-f docker-compose.loadtest.yml \
		--profile loadtest \
		run --rm \
		-e K6_VUS=200 \
		-e K6_DURATION=3m \
		-e K6_STRESS=true \
		k6 run \
		  --stage 0:200 \
		  --stage 180:200 \
		  --stage 10:0 \
		  /scripts/load_test.js

load-test-down:
	docker compose \
		-f docker-compose.yml \
		-f docker-compose.loadtest.yml \
		--profile loadtest \
		down -v

# ─── Developer environment ────────────────────────────────────────────────────
# One-command onboarding for any contributor. Installs the pinned tool versions
# (matching CI) and the local git hooks, so "green locally == green in CI".
# See CONTRIBUTING.md.
setup: tools hooks
	@echo ""
	@echo "✓ Dev environment ready: pinned tools installed and git hooks active."
	@echo "  Commits now run the same lint/build/security/test checks as CI."

# Install the pinned dev tools (lefthook, golangci-lint, gitleaks, goimports).
tools:
	@bash scripts/install-tools.sh

# Install local git hooks (requires lefthook — `make tools` installs it).
hooks:
	lefthook install

# Run the full pre-commit gate on demand against the whole tree — the exact
# checks the git hook runs (the hook itself only checks staged files).
# Handy before opening a PR, or to reproduce a hook failure.
verify:
	lefthook run pre-commit --all-files
