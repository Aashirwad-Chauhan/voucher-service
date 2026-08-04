# Idempotent Voucher Redemption Service — Technical Write-Up

**Candidate**: Aashirwad Chauhan  
**Repository**: [github.com/Aashirwad-Chauhan/voucher-service](https://github.com/Aashirwad-Chauhan/voucher-service)  
**Live URL**: `https://voucher-service-production-aashirwad.onrender.com` *(populated after live deployment)*  
**Date**: August 4, 2026  
**Total Cost**: ₹0 (100% Free Tiers)

---

## 1. Data Model & Schema Design

The service is backed by PostgreSQL 16 with schema enforced via migration scripts (`migrations/000001_init.up.sql`).

```
┌─────────────────────────────────────────┐
│                vouchers                 │
├─────────────────────────────────────────┤
│ id              UUID        PRIMARY KEY │
│ code            TEXT        UNIQUE      │
│ max_redemptions INT         CHECK > 0   │
│ remaining       INT         CHECK >= 0  │
│ created_at      TIMESTAMPTZ NOT NULL    │
└────────────────────┬────────────────────┘
                     │ 1
                     │
                     │ N
┌────────────────────▼────────────────────┐
│               redemptions               │
├─────────────────────────────────────────┤
│ id              UUID        PRIMARY KEY │
│ voucher_id      UUID        REFERENCES  │
│ user_id         TEXT        NOT NULL    │
│ idempotency_key TEXT        UNIQUE      │
│ created_at      TIMESTAMPTZ NOT NULL    │
└─────────────────────────────────────────┘

┌─────────────────────────────────────────┐
│            idempotency_keys             │
├─────────────────────────────────────────┤
│ key             TEXT        PRIMARY KEY │
│ fingerprint     TEXT        NOT NULL    │
│ voucher_code    TEXT        NOT NULL    │
│ response_code   INT         NOT NULL    │
│ response_body   JSONB       NOT NULL    │
│ created_at      TIMESTAMPTZ NOT NULL    │
└─────────────────────────────────────────┘
```

### Key Schema Invariants & Defense-in-Depth
- `CHECK (remaining >= 0)`: Database-level guarantee that `remaining` can NEVER drop below zero, regardless of application code or concurrent race conditions.
- `CHECK (remaining <= max_redemptions)`: Guarantees remaining uses never exceed the maximum allowed.
- `UNIQUE (idempotency_key)` on `redemptions`: Ensures a single idempotency key can never produce more than one row in the append-only audit trail.

---

## 2. Concurrency Control & Correctness Under Burst

### The Core Mechanism: Atomic Compare-and-Swap (CAS) Update
Redemption correctness under high concurrency relies on PostgreSQL's row-level lock execution semantics using a single conditional SQL statement:

```sql
UPDATE vouchers
SET remaining = remaining - 1
WHERE code = $1 AND remaining > 0
RETURNING id, remaining;
```

### How PostgreSQL Handles a Concurrent Burst
When 50 requests attempt to redeem a single-use voucher (`remaining = 1`) simultaneously:
1. PostgreSQL locks the voucher row for the first `UPDATE` statement that executes.
2. The remaining 49 concurrent transactions wait for row lock release.
3. Transaction 1 executes: `remaining` transitions from `1 → 0`. The updated row is returned.
4. Transaction 1 inserts into `redemptions` and `idempotency_keys` and commits.
5. Transactions 2..50 acquire the row lock sequentially, but evaluate `WHERE remaining > 0` as `FALSE`.
6. PostgreSQL returns `0` affected rows (`pgx.ErrNoRows`) for transactions 2..50.
7. The application instantly recognizes `0` rows affected, rolls back, and returns `HTTP 422 Unprocessable Entity`.

### Tradeoff Analysis & Alternatives Considered

| Mechanism | Description | Pros | Cons | Verdict |
|-----------|-------------|------|------|---------|
| **Pessimistic Locking** (`SELECT ... FOR UPDATE`) | Lock voucher row before checking remaining | Intuitive sequence | Serializes throughput; potential deadlocks if multiple vouchers locked in different order | Functional, but heavier overhead |
| **Atomic CAS** (`UPDATE ... WHERE remaining > 0 RETURNING`) | Conditional single-statement update | **Zero explicit locks**, maximum throughput, no deadlocks, single-statement atomicity | Requires handling `0` rows returned | **CHOSEN** |
| **Optimistic Versioning** (`version = version + 1`) | Include version in WHERE clause | No DB locks | High retry storm overhead under dense bursts | Wastes resources under burst |
| **Redis Counter + Lua Script** | `DECR` counter in Redis via Lua script | Sub-millisecond locking | **Dual-Write Problem**: Redis counter decrements, but if Postgres audit log insert fails, state becomes inconsistent | Excellent for high-scale multi-region, overkill for single-node ACID scope |

---

## 3. Idempotency Key Design

### Storage & Matching
Every redemption request carries an `idempotency_key`. The service enforces idempotency in the repository transaction before attempting any mutation:

1. **Fingerprint Calculation**: Computed as SHA-256 hash of `voucher_code + ":" + user_id`.
2. **Lookup**: `SELECT fingerprint, response_code, response_body FROM idempotency_keys WHERE key = $1`.
3. **Replay (Same Key + Same Body)**: If key exists and `fingerprint == expectedFingerprint`, immediately return the stored HTTP 200 response JSON. `remaining` remains untouched.
4. **Conflict (Same Key + Different Body)**: If key exists and `fingerprint != expectedFingerprint`, reject with `HTTP 409 Conflict`.
5. **Fresh Request**: If key is absent, proceed with atomic redemption and record key + response body upon commit.

### Idempotency Expiration Strategy
In production environments, idempotency keys can be purged via a scheduled daily worker: `DELETE FROM idempotency_keys WHERE created_at < now() - INTERVAL '24 hours'`.

---

## 4. How Tests Prove the Invariant

The test suite contains both unit tests and high-concurrency integration tests (`internal/repository/postgres_test.go`).

### High-Concurrency Race Test (`TestConcurrentRedeem_SingleUse`)
- **Setup**: Creates a single-use voucher (`max_redemptions = 1`).
- **Execution**: Spawns **50 concurrent goroutines**. A synchronization channel (`gate`) holds all goroutines until all are spawned, then releases them simultaneously.
- **Assertions**:
  - `successes == 1`: Exactly 1 request receives HTTP 200 OK.
  - `exhausted == 49`: Exactly 49 requests receive HTTP 422 Unprocessable Entity.
  - `otherErrors == 0`: Zero 500 internal server errors.
  - `remaining == 0` & `redemptions_count == 1` in PostgreSQL.

---

## 5. Edge Cases Handled

| Edge Case | Request State | HTTP Code | Error Response |
|-----------|---------------|-----------|----------------|
| Exhausted Voucher | `remaining == 0` | `422` | `{"error": "voucher_exhausted", "message": "Voucher has no remaining redemptions"}` |
| Unknown Voucher Code | Code not in DB | `404` | `{"error": "voucher_not_found", "message": "Voucher code not found"}` |
| Idempotency Replay | Same key + same payload | `200` | Original `RedeemResponse` (replay flag in telemetry) |
| Idempotency Conflict | Same key + different payload | `409` | `{"error": "idempotency_conflict", "message": "Idempotency key was used with a different request body"}` |
| Concurrent Burst | 50 simultaneous requests | 1x `200`, 49x `422` | Clean rejection, 0x `500` |
| Duplicate Voucher Code | `POST /vouchers` with existing code | `409` | `{"error": "voucher_already_exists", "message": "Voucher code already exists"}` |

---

## 6. Containerization, Deployment & Observability

### Multi-Stage Containerization (`Dockerfile`)
- **Stage 1 (Build)**: `golang:1.24-alpine` builds statically linked Linux binary (`CGO_ENABLED=0`).
- **Stage 2 (Runtime)**: Minimal `alpine:3.21` image (~15MB final size).
- **Security**: Operates as non-root user (`appuser`, UID 1001).
- **Healthcheck**: Built-in container `HEALTHCHECK` using `wget` against `/healthz`.

### 12-Factor Configuration & Deployment
- Hosted on **Render.com** (Docker runtime) backed by **Neon PostgreSQL 16**.
- All configuration supplied via environment variables (`DATABASE_URL`, `PORT`, `LOG_LEVEL`, Grafana credentials).

### Observability Architecture
- **Structured JSON Logging**: Handled via stdlib `log/slog`. Every log entry emits JSON with timestamp, level, event type, and `correlation_id`.
- **Correlation ID Middleware**: Every request extracts or generates an `X-Correlation-ID` header, propagated through `context.Context` to all log lines and HTTP headers.
- **Log Shipping**: Asynchronous push client (`internal/observability/loki.go`) ships JSON logs directly to **Grafana Cloud Loki**.
- **Prometheus Telemetry**: `/metrics` endpoint exposes HTTP request counts, p99 request duration histograms, and redemption outcome counters (`voucher_redemptions_total{result="granted|rejected_exhausted|replay|conflict"}`).

---

## 7. AI Usage Disclosure (Directed vs. Decided)

### What the Developer Decided
- **Architecture**: Selected PostgreSQL single-statement atomic CAS (`UPDATE ... WHERE remaining > 0 RETURNING`) over Redis dual-write to eliminate data inconsistency windows.
- **HTTP Semantics**: Selected `HTTP 422 Unprocessable Entity` for exhausted vouchers to cleanly distinguish client semantic state from bad requests (`400`) or missing resources (`404`).
- **Idempotency Fingerprinting**: Designed SHA-256 fingerprint matching (`code:user_id`) to detect same-key payload conflicts.
- **Database Safeguards**: Defined strict SQL `CHECK` constraints on `remaining` and `max_redemptions` as defense-in-depth.

### What AI Assisted / Directed
- Generated initial Go package boilerplate and SQL schema templates.
- Wrote initial draft for `docker-compose.yml` healthcheck parameters.
- Assisted with Grafana Loki push API payload formatting.

---

## 8. Cost Breakdown

| Component | Platform | Plan | Cost |
|-----------|----------|------|------|
| Web Service | Render.com | Free Tier | ₹0 / $0 |
| PostgreSQL Database | Neon.tech | Serverless Free Tier | ₹0 / $0 |
| Log Aggregation | Grafana Cloud Loki | Free Tier (50GB/mo) | ₹0 / $0 |
| Prometheus Metrics | Grafana Cloud Prometheus | Free Tier | ₹0 / $0 |
| **Total** | | | **₹0 / $0** |
