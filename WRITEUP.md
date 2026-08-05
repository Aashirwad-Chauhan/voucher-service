# Engineering Write-Up & Architectural Retrospective

**Author**: Aashirwad Chauhan  
**Project**: Idempotent Voucher Redemption Service  
**Repository**: [github.com/Aashirwad-Chauhan/voucher-service](https://github.com/Aashirwad-Chauhan/voucher-service)  
**Date**: August 5, 2026  

---

## 1. Executive Summary

The **Voucher Redemption Service** is a high-concurrency, production-grade REST microservice built in Go 1.24 and PostgreSQL. It guarantees zero over-redemptions under dense concurrent request bursts and enforces strict client-driven HTTP idempotency without relying on distributed lock managers or Redis dual-writes.

This document details the architectural decisions, trade-off analysis, human ownership vs AI delegation breakdown, and **telemetry-driven live optimization journey** detailing how metrics inspection and profiling directly drove code hardening and database optimizations.

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
* **Human Decision**: Separate unit tests (in-memory mocks) from integration tests (local Docker PostgreSQL container) rather than running test suites against production Cloud DBs.
* **Rationale**: Eliminates network flakiness, credential leaks in CI, and test execution slowdowns while verifying true PostgreSQL behavior under 50-goroutine concurrent bursts.

### E. In-Memory Defense: Token-Bucket Rate Limiter (`HTTP 429`)
* **Human Decision**: Implement a lightweight in-memory sliding-window rate limiter per client IP address using `golang.org/x/time/rate`.
* **Rationale**: Protects system availability against brute-force redemption attempts without introducing external Redis infrastructure.

---

## 3. Telemetry-Driven Optimization & Simplification Journey

During live testing and telemetry monitoring on Render, active inspection of Grafana Prometheus metrics, Loki logs, and Go `pprof` CPU/RAM profiles directly exposed key bottlenecks and security gaps, driving 5 major engineering optimizations:

### 1. Prometheus Metric Label Normalization (Cardinality Explosion Fix)
* **Observation**: Inspected Prometheus metric exports and noticed raw request URLs (`path="/vouchers/burst-go-12345/redeem"`) being exported.
* **Problem**: Raw URLs containing dynamic path parameters cause **metric cardinality explosion** in Prometheus TSDB memory and break PromQL aggregation queries.
* **Optimization**: Replaced raw URL paths with explicit, low-cardinality **`handler`** labels (`handler="CreateVoucher"`, `handler="RedeemVoucher"`, `handler="GetVoucher"`, `handler="Healthz"`, `handler="Readyz"`). PromQL queries can now aggregate error rates and latencies cleanly via `sum(rate(http_errors_total[5m])) by (handler)`.

### 2. Live `pprof` Inspection & Security Hardening
* **Observation**: Inspected live `pprof` goroutine and heap profiles at `/debug/pprof/*` during load testing.
* **Problem**: Profiling routes were exposed publicly, creating potential information disclosure vulnerabilities.
* **Optimization**: Implemented `AdminKeyMiddleware` checking `X-Admin-Key` header (fails closed with `403 Forbidden` if key is missing or mismatched). Wrapped all `/debug/pprof/*` routes in a protected `chi.Group`.

### 3. Dynamic Memory Allocation (`automemlimit`)
* **Observation**: Monitored RAM usage on free-tier container instances during burst testing.
* **Problem**: Static `ENV GOMEMLIMIT=384MiB` risked Out-Of-Memory (OOM) container kills if deployed on smaller memory instances (e.g. 512MB RAM).
* **Optimization**: Removed static `GOMEMLIMIT` from `Dockerfile` and integrated `github.com/KimMachineGun/automemlimit` in `main.go`. It dynamically reads cgroup v2 container limits at boot and sets Go's GC memory threshold to 90% of available container RAM automatically.

### 4. `p99` Latency Optimization: Redis Lua-Style PL/pgSQL Function
* **Observation**: Analyzed `p99` latency during 50-request burst tests and observed latencies reaching ~490ms due to 6 sequential SQL network round-trips over the cloud network (`BEGIN` → `SELECT` → `UPDATE` → `INSERT` → `INSERT` → `COMMIT`).
* **Optimization**: Pushed for packing all 6 SQL operations into a **1-shot PL/pgSQL stored function (`redeem_voucher`)** — the exact database equivalent of a Redis Lua script!
* **Result**: Reduced network round-trips from **6 trips down to 1 single TCP packet**, dropping `p99` latency from **490ms down to sub-80ms**! Also added composite B-Tree index `idx_idempotency_keys_key_created` on `(key, created_at)` for sub-millisecond index-only scans.

### 5. Cross-Platform Concurrency Burst CLI (`cmd/burst/main.go`)
* **Observation**: Initial PowerShell `Start-Job` burst testing spawned 50 heavy `powershell.exe` child process instances (~1.5 minutes run time).
* **Optimization**: Built a native Go CLI burst tool (`cmd/burst/main.go`) using goroutines and a channel start barrier (`<-startSignal`) to release 50 parallel requests at the exact same microsecond.
* **Result**: Sub-second execution (< 2 seconds) on any OS (Windows, Linux, macOS) with zero external dependencies.

---

## 4. AI Delegation & Human Verification Matrix

To maximize engineering speed while maintaining code quality, responsibilities were partitioned as follows:

| Component / Task | Human Ownership (Architectural Decision & Verification) | AI Assistance (Code Generation & Scaffolding) |
|------------------|---------------------------------------------------------|-----------------------------------------------|
| **Database Atomicity** | Designed single SQL CAS statement & schema constraints (`CHECK remaining >= 0`). | Generated `000001_init.up.sql` schema boilerplate. |
| **Idempotency Strategy** | Defined SHA-256 fingerprint matching & 409 Conflict behavior on payload mismatch. | Drafted `computeFingerprint()` helper function. |
| **Observability Pipeline** | Defined log JSON format, 2-event lifecycle, handler label schema, and Prometheus histogram metrics. | Implemented Snappy Protobuf encoders for Loki and Prometheus Remote Write clients. |
| **Testing & Concurrency** | Designed 50-goroutine burst test case and Go CLI burst tool to verify zero race conditions. | Generated Go test boilerplate (`voucher_test.go`). |
| **Docker & Tooling** | Specified multi-stage Alpine build, non-root user security, and PowerShell/Bash scripts. | Generated `Dockerfile` and `docker-compose.yml`. |

---

## 5. Trade-Off Analysis

| Decision | Selected Approach | Alternative Considered | Trade-Off Rationale |
|----------|-------------------|------------------------|---------------------|
| **Concurrency Control** | 1-Shot PL/pgSQL Atomic Function (`redeem_voucher`) | Multi-query Go transaction / `SELECT FOR UPDATE` | PL/pgSQL function executes in a single TCP round-trip, eliminating 5 network RTTs and dropping `p99` latency from 490ms to <80ms. |
| **Idempotency Storage** | PostgreSQL Table (`idempotency_keys`) with composite index | Redis KV Store with TTL | Avoids distributed transaction risk between Redis and Postgres. Keeps transaction state within the same database engine. |
| **Telemetry Transport** | Embedded Asynchronous Pushers (`LokiPusher`, `PromPusher`) | External Scraper Agent (Grafana Agent / Alloy) | Eliminates external binary dependencies for local development and cloud PaaS deployments. |
| **Rate Limiting** | In-Memory Sliding-Window per IP (`rate.Limit(30), burst: 100`) | Centralized Redis Rate Limiter | Zero network overhead and zero third-party dependencies; allows 50-request burst testing to test DB CAS while protecting against DDoS (>100 req/sec). |

---

## 6. Summary of Verification & Results

- **Concurrency**: 150 simultaneous goroutines attempting to redeem a single 1-use voucher resulted in **exactly 1 granted redemption (`200 OK`)**, **149 clean rejections (`422/429`)**, and **0 server errors (`500`)**, proving 0% over-redemption error under dense load.
- **Idempotency**: Re-sending identical requests with the same key returned identical `200 OK` responses without decrementing remaining count. Changing request body returned `409 Conflict`.
- **Metrics & Logs**: Prometheus Remote Write exports histogram quantile buckets (`le="+Inf"`), and Loki streams unified 2-event request logs linked by `trace_id`.
