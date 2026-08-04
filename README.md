# Idempotent Voucher Redemption Service

A robust, high-concurrency voucher redemption service built with **Go**, **PostgreSQL**, and **Docker**.

Designed to handle concurrent redemption bursts with **atomic CAS updates**, zero over-redemptions, strict idempotency enforcement, structured JSON logging, and Prometheus telemetry.

---

## Features

- ⚡ **Atomic Concurrency Control**: Uses PostgreSQL `UPDATE ... WHERE remaining > 0 RETURNING` for zero over-redemption guarantees under concurrent bursts.
- 🔁 **Strict Idempotency**: Guarantees identical response replays for matching keys, and returns `HTTP 409 Conflict` for key payload mismatches.
- 📦 **Containerized & Minimal**: Multi-stage `Dockerfile` creating a tiny (~15MB) Alpine image running as a non-root user with healthchecks.
- 📊 **Full Observability**: Structured JSON logging (`slog`), request correlation IDs (`X-Correlation-ID`), Grafana Loki integration, and Prometheus telemetry (`/metrics`).
- 🛡️ **Defense-in-Depth**: Database-level `CHECK (remaining >= 0)` constraints prevent invalid state transitions even if application code fails.

---

## API Specification

| Method | Route | Request Body | Description | Success Status |
|--------|-------|--------------|-------------|----------------|
| `POST` | `/vouchers` | `{"code": "SUMMER2026", "max_redemptions": 5}` | Create new voucher | `201 Created` |
| `POST` | `/vouchers/{code}/redeem` | `{"user_id": "u123", "idempotency_key": "k456"}` | Redeem voucher use | `200 OK` |
| `GET`  | `/vouchers/{code}` | — | Get voucher status & count | `200 OK` |
| `GET`  | `/healthz` | — | Process liveness probe | `200 OK` |
| `GET`  | `/readyz` | — | Database readiness probe | `200 OK` / `503` |
| `GET`  | `/metrics` | — | Prometheus metrics | `200 OK` |

---

## Quick Start (Local Development)

### Prerequisites
- Docker & Docker Compose
- Go 1.24+ (optional for local non-containerized testing)

### Run App + Database with Docker Compose
```bash
# Bring up app and PostgreSQL database
docker compose up --build
```

The service will start at `http://localhost:8080`.

---

## Testing & Concurrency Verification

### Run Unit & Integration Tests
```bash
# Run all unit tests
go test ./internal/service ./internal/handler -v

# Run race condition integration tests against PostgreSQL
$env:TEST_DATABASE_URL="postgres://voucher:voucher@localhost:5432/voucher?sslmode=disable"
go test ./internal/repository -v -count=1
```

### Run Concurrency Burst Gate Script
```bash
# Linux / macOS / Bash
./scripts/burst.sh http://localhost:8080 50

# Windows PowerShell
.\scripts\burst.ps1 -BaseUrl "http://localhost:8080" -Concurrency 50
```

This script creates a single-use voucher and fires 50 simultaneous requests. It verifies that **exactly 1 request succeeds (HTTP 200)**, **49 requests are cleanly rejected (HTTP 422)**, and **0 server errors occur (HTTP 5xx)**.

---

## Documentation & Deep Dive

For detailed technical analysis, architectural decision trade-offs, idempotency fingerprinting details, and AI usage breakdown, please read:
- [WRITEUP.md](WRITEUP.md)
