# Voucher Redemption Service

[![Go Version](https://img.shields.io/badge/Go-1.24-00ADD8?style=for-the-badge&logo=go)](https://golang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=for-the-badge&logo=postgresql)](https://www.postgresql.org)
[![Docker](https://img.shields.io/badge/Docker-Enabled-2496ED?style=for-the-badge&logo=docker)](https://www.docker.com)
[![Grafana](https://img.shields.io/badge/Grafana-Loki%20%26%20Prometheus-F46800?style=for-the-badge&logo=grafana)](https://grafana.com)

An idempotent, high-concurrency voucher redemption microservice built with **Go**, **PostgreSQL**, and **Docker**. Designed to handle dense concurrent request bursts with **zero over-redemptions**, strict client-driven idempotency protection, in-memory rate limiting, and unified Grafana Loki & Prometheus telemetry.

---

## 📋 Table of Contents

- [Features](#-features)
- [Architecture & Sequence Diagrams](#-architecture--sequence-diagrams)
  - [1. Voucher Creation Flow](#1-voucher-creation-flow)
  - [2. Atomic Redemption & Idempotency Flow](#2-atomic-redemption--idempotency-flow)
  - [3. Voucher Status Flow](#3-voucher-status-flow)
- [Prerequisites & Installation](#-prerequisites--installation)
- [Running the Application](#-running-the-application)
  - [Option A: Via Docker Compose (Recommended)](#option-a-via-docker-compose-recommended)
  - [Option B: Running Locally with Go](#option-b-running-locally-with-go)
- [Running Tests](#-running-tests)
  - [Unit Tests](#unit-tests)
  - [Concurrent Integration Tests](#concurrent-integration-tests)
- [Testing via cURL & Postman](#-testing-via-curl--postman)
- [Observability & Monitoring](#-observability--monitoring)
- [Key Architectural Decisions](#-key-architectural-decisions)
- [Project Structure](#-project-structure)

---

## ✨ Features

- **Atomic CAS Redemptions**: Prevents double-spending and race conditions under heavy concurrency using single-statement SQL Compare-and-Swap (`UPDATE ... WHERE remaining > 0 RETURNING`).
- **Client-Driven Idempotency**: Prevents double-redemptions on client network retries using client-generated `idempotency_key` and SHA-256 request fingerprinting. Re-using a key with a mismatched body returns `409 Conflict`.
- **24-Hour Idempotency TTL**: Automatically expires and purges old idempotency records via an hourly background worker.
- **In-Memory Rate Limiting**: Per-IP sliding-window rate limiter (`300 req/min`) returning `429 Too Many Requests`.
- **Unified Telemetry**: Clean 2-event request logging (`request_received` and `request_completed`) linked by a single `trace_id` sent asynchronously to Grafana Loki & Prometheus Remote Write.
- **Production Containerization**: Multi-stage Dockerfile (~15MB Alpine runtime) running as non-root `appuser` with container `HEALTHCHECK`.

---

## 🏗️ Architecture & Sequence Diagrams

### 1. Voucher Creation Flow (`POST /vouchers`)

```mermaid
sequenceDiagram
    autonumber
    actor Client
    participant API as Voucher API (Chi)
    participant Repo as Postgres Repository
    participant DB as PostgreSQL DB

    Client->>API: POST /vouchers (code, max_redemptions)
    API->>API: Validate input (code != "", max_redemptions >= 1)
    alt Validation Error
        API-->>Client: 400 Bad Request
    end
    API->>Repo: CreateVoucher(code, max_redemptions)
    Repo->>DB: INSERT INTO vouchers (code, max_redemptions, remaining)...
    alt Code Already Exists
        DB-->>Repo: Unique Constraint Violation (23505)
        Repo-->>API: ErrDuplicateVoucher
        API-->>Client: 409 Conflict (already_exists)
    else Successfully Created
        DB-->>Repo: Returns voucher ID, code, remaining
        Repo-->>API: Voucher Domain Model
        API-->>Client: 201 Created (JSON Response)
    end
```

---

### 2. Atomic Redemption & Idempotency Flow (`POST /vouchers/{code}/redeem`)

```mermaid
sequenceDiagram
    autonumber
    actor Client
    participant API as Voucher API
    participant Repo as Postgres Repository
    participant DB as PostgreSQL DB

    Client->>API: POST /vouchers/{code}/redeem (user_id, idempotency_key)
    API->>API: Extract X-Trace-ID & Validate Payload
    API->>Repo: RedeemVoucher(code, user_id, idempotency_key)
    Repo->>DB: BEGIN TRANSACTION

    Note over Repo,DB: Step 1: Idempotency Key Check
    Repo->>DB: SELECT fingerprint, response_body FROM idempotency_keys WHERE key = $1 AND created_at > NOW() - 24h
    alt Key Exists & Fingerprint Matches
        DB-->>Repo: Return Cached Response Body
        Repo->>DB: ROLLBACK
        Repo-->>API: Replay Response (isReplay = true)
        API-->>Client: 200 OK (Cached Response + Replay Header)
    else Key Exists & Mismatched Fingerprint
        Repo->>DB: ROLLBACK
        Repo-->>API: ErrIdempotencyConflict
        API-->>Client: 409 Conflict (idempotency_conflict)
    end

    Note over Repo,DB: Step 2: Atomic CAS Decrement
    Repo->>DB: UPDATE vouchers SET remaining = remaining - 1 WHERE code = $1 AND remaining > 0 RETURNING id, remaining
    alt remaining == 0 (Exhausted or Unknown Code)
        Repo->>DB: SELECT remaining FROM vouchers WHERE code = $1
        alt Code Not Found
            Repo->>DB: ROLLBACK
            API-->>Client: 404 Not Found
        else Code Exhausted (remaining == 0)
            Repo->>DB: ROLLBACK
            API-->>Client: 422 Unprocessable Entity (rejected_exhausted)
        end
    else CAS Update Succeeded (remaining >= 0)
        Repo->>DB: INSERT INTO redemptions (voucher_id, user_id, idempotency_key)...
        Repo->>DB: INSERT INTO idempotency_keys (key, fingerprint, response_code, response_body)...
        Repo->>DB: COMMIT TRANSACTION
        Repo-->>API: RedeemResponse (granted)
        API-->>Client: 200 OK (fresh redemption)
    end
```

---

### 3. Voucher Status Flow (`GET /vouchers/{code}`)

```mermaid
sequenceDiagram
    autonumber
    actor Client
    participant API as Voucher API
    participant Repo as Postgres Repository
    participant DB as PostgreSQL DB

    Client->>API: GET /vouchers/{code}
    API->>Repo: GetVoucher(code)
    Repo->>DB: SELECT id, code, remaining, redemptions_count, max_redemptions FROM vouchers WHERE code = $1
    alt Voucher Not Found
        DB-->>Repo: pgx.ErrNoRows
        Repo-->>API: ErrVoucherNotFound
        API-->>Client: 404 Not Found
    else Voucher Found
        DB-->>Repo: Row data
        Repo-->>API: VoucherStatusResponse
        API-->>Client: 200 OK (Voucher Details)
    end
```

---

## ⚡ Prerequisites & Installation

### Prerequisites
- **Go**: 1.24+ ([Download Go](https://golang.org/dl/))
- **Docker & Docker Compose**: ([Download Docker](https://www.docker.com/products/docker-desktop/))
- **Git**: For version control

### Clone Repository
```bash
git clone https://github.com/Aashirwad-Chauhan/voucher-service.git
cd voucher-service
```

---

## 🚀 Running the Application

### Option A: Via Docker Compose (Recommended)

Start both the API server and PostgreSQL database in isolated containers:

```bash
# Build and start services
docker compose up --build

# View container status
docker compose ps

# Stop services
docker compose down -v
```

The server will automatically run migrations and start listening at `http://localhost:8080`.

---

### Option B: Running Locally with Go

1. **Start PostgreSQL Database**:
   ```bash
   docker run --name voucher-db -e POSTGRES_USER=voucher -e POSTGRES_PASSWORD=voucher -e POSTGRES_DB=voucher -p 5432:5432 -d postgres:16-alpine
   ```

2. **Run Application**:
   ```bash
   # Set environment variables
   export DATABASE_URL="postgres://voucher:voucher@localhost:5432/voucher?sslmode=disable"
   export PORT="8080"
   export LOG_LEVEL="info"

   # Run server
   go run ./cmd/server
   ```

---

## 🧪 Running Tests

### Unit Tests
Executes fast in-memory service, handler, and configuration unit tests:
```bash
go test ./internal/config ./internal/service ./internal/handler -v -cover
```

### Concurrent Integration Tests & Concurrency Burst Tool
Executes the **50-goroutine burst test** against PostgreSQL to verify zero over-redemption race conditions:
```bash
# Set test database environment variable
export TEST_DATABASE_URL="postgres://voucher:voucher@localhost:5432/voucher?sslmode=disable"

# Run integration tests
go test ./internal/repository -v -count=1
```

#### Run Live Concurrency Burst Tool (Cross-Platform Go CLI)
To test dense parallel traffic (50 simultaneous requests) against a local or production server:
```bash
# Test against live Render production deployment:
go run ./cmd/burst https://voucher-service-c7kf.onrender.com 50

# Test against local server:
go run ./cmd/burst http://localhost:8080 50
```

Or run all tests at once via `Makefile`:
```bash
make test
```

---

## 📄 Testing via cURL & Postman

A complete **cURL Cheatsheet** is available in [`CURL_CHEATSHEET.md`](file:///c:/Users/ASUS/OneDrive/Desktop/Repos/voucher-service/CURL_CHEATSHEET.md), and a **Postman Collection** is included in `postman_collection.json`.

### Sample Quick cURL Commands

#### 1. Create a Voucher (Max 3 Redemptions)
```bash
curl -X POST http://localhost:8080/vouchers \
  -H "Content-Type: application/json" \
  -d '{"code": "WELCOME50", "max_redemptions": 3}'
```

#### 2. Redeem Voucher
```bash
curl -X POST http://localhost:8080/vouchers/WELCOME50/redeem \
  -H "Content-Type: application/json" \
  -H "X-Trace-ID: test-trace-001" \
  -d '{"user_id": "user-101", "idempotency_key": "txn-key-001"}'
```

#### 3. Test Idempotent Replay (Re-send Same Key)
```bash
curl -X POST http://localhost:8080/vouchers/WELCOME50/redeem \
  -H "Content-Type: application/json" \
  -d '{"user_id": "user-101", "idempotency_key": "txn-key-001"}'
```

#### 4. Check Voucher Status
```bash
curl -X GET http://localhost:8080/vouchers/WELCOME50
```

---

## 📊 Observability & Monitoring

The service includes zero-dependency background telemetry pushers:

1. **Grafana Loki (`LokiPusher`)**:
   - Environment variables: `GRAFANA_LOKI_URL`, `GRAFANA_LOKI_USER`, `GRAFANA_API_KEY`.
   - Streams unified JSON logs with `trace_id`, `latency_ms`, and `status`.

2. **Grafana Prometheus Remote Write (`PromPusher`)**:
   - Environment variables: `GRAFANA_PROM_URL`, `GRAFANA_PROM_USER`, `GRAFANA_PROM_KEY`.
   - Exports metric series:
     - `http_requests_total{status, method, path}`
     - `http_errors_total{status, method, path}`
     - `http_request_duration_seconds_bucket{le}` (p50, p90, p99 latencies)
     - `voucher_redemptions_total{result}`

---

## 💡 Key Architectural Decisions

1. **PostgreSQL Atomic CAS Over Redis**: Avoided dual-write state drift by using single SQL Compare-and-Swap statements.
2. **SHA-256 Idempotency Fingerprinting**: Replays matching requests (`200 OK`) and rejects payload key mismatches (`409 Conflict`).
3. **24-Hour Idempotency TTL**: Background hourly worker purges expired keys to maintain minimal index size.
4. **In-Memory Rate Limiter**: Enforces sliding-window IP rate limits (`HTTP 429`) without external Redis dependencies.

For a full deep-dive into trade-offs and AI delegation details, see [`WRITEUP.md`](file:///c:/Users/ASUS/OneDrive/Desktop/Repos/voucher-service/WRITEUP.md).

---

## 📁 Project Structure

```
voucher-service/
├── cmd/
│   ├── server/
│   │   └── main.go              # Server entrypoint & dependency wiring
│   └── burst/
│       └── main.go              # High-concurrency burst testing CLI tool
├── internal/
│   ├── config/                  # Environment configuration loader
│   ├── handler/                 # HTTP handlers, middleware, rate limiter
│   ├── model/                   # Domain entities and error types
│   ├── observability/           # Grafana Loki & Prometheus Remote Write pushers
│   ├── repository/              # PostgreSQL repository & atomic CAS logic
│   └── service/                 # Business logic & validation layer
├── migrations/                  # Embedded SQL migration scripts
├── Dockerfile                   # Multi-stage Alpine container build
├── docker-compose.yml           # Local orchestration (App + Postgres)
├── Makefile                     # Build & test automation shortcuts
├── postman_collection.json      # Importable Postman v2.1 collection
├── CURL_CHEATSHEET.md           # Copy-paste cURL test commands
├── WRITEUP.md                   # Engineering retrospective & trade-off analysis
└── README.md                    # Main project documentation
```
