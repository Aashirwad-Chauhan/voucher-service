.PHONY: test test-unit test-integration docker-up docker-down burst run

# Default target: run unit tests
test: test-unit

# Run unit tests only (no DB required, instant)
test-unit:
	go test ./internal/service ./internal/handler -v

# Run integration tests against local Docker PostgreSQL
test-integration:
	docker compose up -d db
	@echo "Waiting for local Postgres to be ready..."
	@docker compose exec db pg_isready -U voucher -t 10
	TEST_DATABASE_URL="postgres://voucher:voucher@localhost:5432/voucher?sslmode=disable" go test ./internal/repository -v -count=1

# Start full local application stack (App + DB)
docker-up:
	docker compose up --build -d

# Stop local stack
docker-down:
	docker compose down -v

# Run concurrent burst test against local app
burst:
	./scripts/burst.sh http://localhost:8080 50

# Run server locally with go run
run:
	go run ./cmd/server
