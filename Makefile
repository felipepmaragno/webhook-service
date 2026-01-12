.PHONY: build run test test-race test-cover lint clean migrate-up migrate-down docker-up docker-down

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

# Docker
docker-up:
	docker-compose up -d

docker-down:
	docker-compose down

docker-logs:
	docker-compose logs -f

# Development
dev: docker-up migrate-up run

# Clean
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html
