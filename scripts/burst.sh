#!/bin/bash
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
CONCURRENCY="${2:-50}"
CODE="burst-$(date +%s)"

echo "=========================================="
echo "   Voucher Service Burst Concurrency Gate "
echo "=========================================="
echo "Target URL:  $BASE_URL"
echo "Concurrency: $CONCURRENCY"
echo "Voucher Code:$CODE"
echo ""

# 1. Create single-use voucher
echo "Step 1: Creating single-use voucher..."
CREATE_RESP=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/vouchers" \
  -H "Content-Type: application/json" \
  -d "{\"code\": \"$CODE\", \"max_redemptions\": 1}")

CREATE_STATUS=$(echo "$CREATE_RESP" | tail -n 1)
CREATE_BODY=$(echo "$CREATE_RESP" | head -n -1)

if [ "$CREATE_STATUS" -ne 201 ]; then
  echo "❌ Failed to create voucher. HTTP $CREATE_STATUS"
  echo "$CREATE_BODY"
  exit 1
fi
echo "Voucher created successfully (HTTP 201)."
echo ""

# 2. Fire concurrent redeems
echo "Step 2: Firing $CONCURRENCY concurrent redeems..."
TMPDIR=$(mktemp -d)

for i in $(seq 1 "$CONCURRENCY"); do
  curl -s -o "$TMPDIR/resp_$i.json" -w "%{http_code}" \
    -X POST "$BASE_URL/vouchers/$CODE/redeem" \
    -H "Content-Type: application/json" \
    -d "{\"user_id\": \"user-$i\", \"idempotency_key\": \"burst-$CODE-$i\"}" \
    > "$TMPDIR/status_$i.txt" &
done
wait

# 3. Analyze HTTP status codes
SUCCESSES=0
REJECTIONS=0
ERRORS=0

for i in $(seq 1 "$CONCURRENCY"); do
  STATUS=$(cat "$TMPDIR/status_$i.txt")
  case "$STATUS" in
    200) SUCCESSES=$((SUCCESSES + 1)) ;;
    422) REJECTIONS=$((REJECTIONS + 1)) ;;
    *)   ERRORS=$((ERRORS + 1))
         echo "  ⚠️ Unexpected status from request $i: HTTP $STATUS"
         cat "$TMPDIR/resp_$i.json"
         echo ""
         ;;
  esac
done

# 4. Fetch final voucher status
echo "Step 3: Verifying final voucher status..."
STATUS_RESP=$(curl -s "$BASE_URL/vouchers/$CODE")

echo ""
echo "=========================================="
echo "              BURST RESULTS               "
echo "=========================================="
echo "Granted Redemptions (HTTP 200): $SUCCESSES"
echo "Clean Rejections     (HTTP 422): $REJECTIONS"
echo "Server Errors        (HTTP 5xx): $ERRORS"
echo ""
echo "Final Voucher State:"
echo "$STATUS_RESP"
echo "=========================================="

rm -rf "$TMPDIR"

if [ "$SUCCESSES" -eq 1 ] && [ "$ERRORS" -eq 0 ]; then
  echo "🎉 SUCCESS: Exactly 1 redemption granted, 0 server errors!"
  exit 0
else
  echo "❌ FAIL: Expected 1 success and 0 server errors."
  exit 1
fi
