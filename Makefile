.PHONY: build run test test-unit test-integration test-e2e test-race test-cover lint validate-ci-local clean migrate-up migrate-down \
        docker-up docker-down docker-logs \
        up down logs seed seed-retry seed-circuit-break validate-basic smoke perf-smoke perf-baseline

ifneq (,$(wildcard .env))
include .env
export
endif

# Build
build:
	go build -o bin/dispatch ./cmd/dispatch

run: build
	./bin/dispatch

# Testing
test:
	go test ./...

test-unit:
	go test -race ./internal/api/... ./internal/config/... ./internal/domain/... ./internal/kafka/... ./internal/observability/... ./internal/retention/... ./internal/retry/...

test-integration:
	go test ./internal/repository/postgres/... ./internal/resilience/...

test-e2e:
	go test ./internal/app/...

test-race:
	go test -race ./...

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Linting
lint:
	golangci-lint run

validate-ci-local: build lint test-unit test-integration test-e2e

# Database
DATABASE_URL ?= postgres://postgres:postgres@localhost:5432/dispatch?sslmode=disable

migrate-up:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/migrate -direction=up

migrate-down:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/migrate -direction=down

# Docker (legacy aliases)
docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

# ── Demo shortcuts ────────────────────────────────────────────────────────────
# Bring up the full stack (builds images if needed).
up:
	docker compose up --build -d
	@echo ""
	@echo "Stack is up. Service URLs:"
	@echo "  API       http://localhost:8090"
	@echo "  Grafana   http://localhost:3000  (admin / admin)"
	@echo "  Receiver  http://localhost:9000"
	@echo "  Prometheus http://localhost:9090"

# Tear down everything and wipe volumes.
down:
	docker compose down -v

# Stream logs from the two application services.
logs:
	docker compose logs -f dispatch-api dispatch-worker

# Seed scenarios — run against a live stack (make up first).
# API_ADDR and RECEIVER_ADDR can be overridden for non-default setups.
API_ADDR             ?= http://localhost:8090
RECEIVER_ADDR        ?= http://receiver:9000
RECEIVER_CONTROL_ADDR ?= http://localhost:9000

seed:
	go run ./cmd/seed \
		--api=$(API_ADDR) \
		--receiver=$(RECEIVER_ADDR) \
		--receiver-control=$(RECEIVER_CONTROL_ADDR) \
		--scenario=normal \
		--events=50 \
		--subs=3

seed-retry:
	go run ./cmd/seed \
		--api=$(API_ADDR) \
		--receiver=$(RECEIVER_ADDR) \
		--receiver-control=$(RECEIVER_CONTROL_ADDR) \
		--scenario=retry \
		--events=30

seed-circuit-break:
	go run ./cmd/seed \
		--api=$(API_ADDR) \
		--receiver=$(RECEIVER_ADDR) \
		--receiver-control=$(RECEIVER_CONTROL_ADDR) \
		--scenario=circuit-break

# Functional smoke validation for the full local stack. This reuses the
# performance harness in smoke mode because it already owns deterministic setup,
# readiness checks, seeding, database assertions, evidence capture, and cleanup.
validate-basic:
	bash ./scripts/performance-validation.sh smoke

smoke: validate-basic

# Performance characterization. Results are written to artifacts/performance/.
perf-smoke:
	bash ./scripts/performance-validation.sh smoke

perf-baseline:
	bash ./scripts/performance-validation.sh baseline

# Development
dev: docker-up migrate-up run

# Clean
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html
