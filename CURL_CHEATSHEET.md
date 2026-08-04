# Voucher Service — cURL Cheatsheet

Set your target URL variable (e.g. `http://localhost:8080` or live URL):

```bash
BASE_URL="http://localhost:8080"
```

---

## 1. Health & Observability

### Liveness Probe (`GET /healthz`)
```bash
curl -i -X GET "$BASE_URL/healthz"
```

### Readiness Probe (`GET /readyz`)
```bash
curl -i -X GET "$BASE_URL/readyz"
```

### Prometheus Metrics (`GET /metrics`)
```bash
curl -s -X GET "$BASE_URL/metrics" | grep -E "http_requests_total|voucher_redemptions_total"
```

---

## 2. Vouchers API

### Create Voucher with `max_redemptions = 3` (`POST /vouchers`)
```bash
curl -i -X POST "$BASE_URL/vouchers" \
  -H "Content-Type: application/json" \
  -d '{
    "code": "SUMMER2026",
    "max_redemptions": 3
  }'
```

### Create Single-Use Voucher (Default `max_redemptions = 1`)
```bash
curl -i -X POST "$BASE_URL/vouchers" \
  -H "Content-Type: application/json" \
  -d '{
    "code": "SINGLE2026"
  }'
```

### Redeem Voucher (`POST /vouchers/{code}/redeem`)
```bash
curl -i -X POST "$BASE_URL/vouchers/SUMMER2026/redeem" \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "user-101",
    "idempotency_key": "txn-key-001"
  }'
```

### Idempotent Replay (Same Key + Same Body -> `200 OK`)
```bash
curl -i -X POST "$BASE_URL/vouchers/SUMMER2026/redeem" \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "user-101",
    "idempotency_key": "txn-key-001"
  }'
```

### Idempotent Conflict (Same Key + Different Payload -> `409 Conflict`)
```bash
curl -i -X POST "$BASE_URL/vouchers/SUMMER2026/redeem" \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "user-DIFFERENT",
    "idempotency_key": "txn-key-001"
  }'
```

### Get Voucher Status (`GET /vouchers/{code}`)
```bash
curl -i -X GET "$BASE_URL/vouchers/SUMMER2026"
```

### Redeem Exhausted Voucher (`422 Unprocessable Entity`)
```bash
# After single-use voucher is redeemed:
curl -i -X POST "$BASE_URL/vouchers/SINGLE2026/redeem" \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "user-102",
    "idempotency_key": "txn-key-002"
  }'
```

### Redeem Unknown Voucher (`404 Not Found`)
```bash
curl -i -X POST "$BASE_URL/vouchers/NONEXISTENT/redeem" \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "user-103",
    "idempotency_key": "txn-key-003"
  }'
```
