# Voucher Redemption Service

An idempotent voucher redemption service built with Go, PostgreSQL, and Docker.

## Quick Start

```bash
# Start app + database
docker compose up --build

# Run tests
go test ./... -v -count=1

# Burst test (concurrent redeems)
./scripts/burst.sh http://localhost:8080
```

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/vouchers` | Create a voucher |
| `POST` | `/vouchers/{code}/redeem` | Redeem a voucher |
| `GET` | `/vouchers/{code}` | Get voucher status |
| `GET` | `/healthz` | Liveness probe |
| `GET` | `/readyz` | Readiness probe |
| `GET` | `/metrics` | Prometheus metrics |

## Architecture

See [IMPLEMENTATION_GUIDE.md](IMPLEMENTATION_GUIDE.md) for full details.
See [WRITEUP.md](WRITEUP.md) for the technical write-up.
