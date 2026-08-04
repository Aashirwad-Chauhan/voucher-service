# Engineering Write-Up & Architectural Retrospective

**Author**: Aashirwad Chauhan  
**Project**: Idempotent Voucher Redemption Service  
**Repository**: [github.com/Aashirwad-Chauhan/voucher-service](https://github.com/Aashirwad-Chauhan/voucher-service)  
**Date**: August 4, 2026  

---

## 1. Executive Summary

The **Voucher Redemption Service** is a high-concurrency, production-grade REST microservice built in Go 1.24 and PostgreSQL. It guarantees zero over-redemptions under dense concurrent request bursts and enforces strict client-driven HTTP idempotency without relying on distributed lock managers or Redis dual-writes.

This document details the architectural decisions, trade-off analysis, human ownership vs AI delegation breakdown, and observability design.

---

## 2. Human Ownership & System Architecture Decisions

While AI was leveraged for boilerplate code generation, test harnesses, and protocol encoders, **all critical architectural decisions, state invariants, data model choices, and observability standards were driven directly by Human Engineering Ownership**:

### A. Architectural Choice: PostgreSQL Atomic CAS vs Redis Dual-Write
* **Human Decision**: Reject Redis for state management and rely strictly on PostgreSQL single-statement Compare-and-Swap (CAS).
* **Rationale**: Distributed dual-writes between Redis (for counting) and PostgreSQL (for audit logs) create eventual consistency windows and dual-write drift risks (e.g., Redis counter decrements, but DB transaction fails due to network partition). Using a single SQL statement `UPDATE vouchers SET remaining = remaining - 1 WHERE code = $1 AND remaining > 0 RETURNING ...` guarantees ACID atomicity at sub-10ms latency without external cache invalidation logic.

### B. Idempotency Key Lifespan & 24-Hour TTL Expiration
* **Human Decision**: Enforce a 24-hour expiration window on idempotency keys paired with an automated background database cleanup worker.
* **Rationale**: Idempotency keys protect against client retry loops after transient network drops. Retaining idempotency keys indefinitely bloats database indexes. By filtering queries with `WHERE created_at > NOW() - INTERVAL '24 hours'` and running an hourly `DELETE` worker, the database maintains high throughput and low index size forever.

### C. Telemetry Standardization: Unified `trace_id` Lifecycle
* **Human Decision**: Consolidate disparate tracking identifiers (`correlation_id`, `trace_id`) into a single mandatory `trace_id` standard, and simplify HTTP request logging into a clean 2-event lifecycle:
  1. `request_received`: Logs input parameters (`code`, `user_id`, `idempotency_key`, `trace_id`).
  2. `request_completed`: Logs final status, latency, result, and remaining count (`status`, `latency_ms`, `result`, `trace_id`).
* **Rationale**: Multiplied log noise degrades Loki query performance. A unified 2-event lifecycle linked by a single `trace_id` enables sub-second log correlation in Grafana Loki.

### D. Testing Strategy: Local Docker Isolation vs Cloud DB
* **Human Decision**: Separate unit tests (in-memory mocks) from integration tests (local Docker PostgreSQL container) rather than running test suites against production Cloud DBs (Neon DB).
* **Rationale**: Eliminates network flakiness, credential leaks in CI, and test execution slowdowns while verifying true PostgreSQL behavior under 50-goroutine concurrent bursts.

### E. In-Memory Defense: Token-Bucket Rate Limiter (`HTTP 429`)
* **Human Decision**: Implement a lightweight in-memory sliding-window rate limiter per client IP address.
* **Rationale**: Protects system availability against brute-force redemption attempts without introducing external Redis infrastructure.

---

## 3. AI Delegation & Human Verification Matrix

To maximize engineering speed while maintaining code quality, responsibilities were partitioned as follows:

| Component / Task | Human Ownership (Architectural Decision & Verification) | AI Assistance (Code Generation & Scaffolding) |
|------------------|---------------------------------------------------------|-----------------------------------------------|
| **Database Atomicity** | Designed single SQL CAS statement & schema constraints (`CHECK remaining >= 0`). | Generated `000001_init.up.sql` schema boilerplate. |
| **Idempotency Strategy** | Defined SHA-256 fingerprint matching & 409 Conflict behavior on payload mismatch. | Drafted `computeFingerprint()` helper function. |
| **Observability Pipeline** | Defined log JSON format, 2-event lifecycle, and Prometheus histogram bucket metrics. | Implemented Snappy Protobuf encoders for Loki and Prometheus Remote Write clients. |
| **Testing & Concurrency** | Designed 50-goroutine burst test case to verify zero race conditions. | Generated Go test boilerplate (`voucher_test.go`). |
| **Docker & Tooling** | Specified multi-stage Alpine build, non-root user security, and PowerShell/Bash scripts. | Generated `Dockerfile` and `docker-compose.yml`. |

---

## 4. Trade-Off Analysis

| Decision | Selected Approach | Alternative Considered | Trade-Off Rationale |
|----------|-------------------|------------------------|---------------------|
| **Concurrency Control** | Single-Statement Atomic CAS (`UPDATE ... WHERE remaining > 0`) | Pessimistic Row Locking (`SELECT FOR UPDATE`) | `SELECT FOR UPDATE` causes lock contention under high concurrency. CAS executes in a single round-trip with zero row lock queues. |
| **Idempotency Storage** | PostgreSQL Table (`idempotency_keys`) | Redis KV Store with TTL | Avoids distributed transaction risk between Redis and Postgres. Keeps transaction state within the same database engine. |
| **Telemetry Transport** | Embedded Asynchronous Pushers (`LokiPusher`, `PromPusher`) | External Scraper Agent (Grafana Agent / Alloy) | Eliminates external binary dependencies for local development and cloud PaaS deployments. |
| **Rate Limiting** | In-Memory Sliding-Window per IP | Centralized Redis Rate Limiter | Zero network overhead and zero third-party dependencies; suitable for single-node / auto-scaled instances. |

---

## 5. Summary of Verification & Results

- **Concurrency**: 50 simultaneous goroutines attempting to redeem a single 5-use voucher resulted in exactly **5 granted redemptions** and **45 exhausted rejections (`422`)**, proving 0% over-redemption error.
- **Idempotency**: Re-sending identical requests with the same key returned identical `200 OK` responses without decrementing remaining count. Changing request body returned `409 Conflict`.
- **Metrics & Logs**: Prometheus Remote Write exports histogram quantile buckets (`le="+Inf"`), and Loki streams unified 2-event request logs linked by `trace_id`.
