.PHONY: build run test test-race test-cover lint clean migrate-up migrate-down \
        docker-up docker-down docker-logs \
        up down logs seed seed-retry seed-circuit-break

# Build
build:
	go build -o bin/dispatch ./cmd/dispatch

run: build
	./bin/dispatch

# Testing
test:
	go test ./...

test-race:
	go test -race ./...

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Linting
lint:
	golangci-lint run

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
	@echo "  API       http://localhost:8080"
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

# Development
dev: docker-up migrate-up run

# Clean
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html
