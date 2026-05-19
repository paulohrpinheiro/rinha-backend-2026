#!/bin/sh
# Benchmark script using Apache Bench (ab)
# Simulates load similar to the official k6 test.
set -eu

BASE_URL="${1:-http://localhost:9999}"
TOTAL="${2:-10000}"
CONCURRENCY="${3:-50}"
READY_URL="${BASE_URL}/ready"
SCORE_URL="${BASE_URL}/fraud-score"

# Sample payloads (4 varieties)
PAYLOAD_A='{"id":"bench-a","transaction":{"amount":384.88,"installments":3,"requested_at":"2026-03-11T20:23:35Z"},"customer":{"avg_amount":769.76,"tx_count_24h":3,"known_merchants":["MERC-009","MERC-001","MERC-001"]},"merchant":{"id":"MERC-001","mcc":"5912","avg_amount":298.95},"terminal":{"is_online":false,"card_present":true,"km_from_home":13.7090520965},"last_transaction":{"timestamp":"2026-03-11T14:58:35Z","km_from_current":18.8626479774}}'
PAYLOAD_B='{"id":"bench-b","transaction":{"amount":2911.41,"installments":12,"requested_at":"2026-03-19T02:17:11Z"},"customer":{"avg_amount":411.03,"tx_count_24h":8,"known_merchants":["MERC-221","MERC-010"]},"merchant":{"id":"MERC-551","mcc":"6011","avg_amount":712.22},"terminal":{"is_online":true,"card_present":false,"km_from_home":2.18},"last_transaction":{"timestamp":"2026-03-18T23:51:05Z","km_from_current":1.34}}'
PAYLOAD_C='{"id":"bench-c","transaction":{"amount":41.12,"installments":2,"requested_at":"2026-03-11T18:45:53Z"},"customer":{"avg_amount":82.24,"tx_count_24h":3,"known_merchants":["MERC-003","MERC-016"]},"merchant":{"id":"MERC-016","mcc":"5411","avg_amount":60.25},"terminal":{"is_online":false,"card_present":true,"km_from_home":29.2331036248},"last_transaction":null}'
PAYLOAD_D='{"id":"bench-d","transaction":{"amount":87.91,"installments":1,"requested_at":"2026-03-13T14:31:30Z"},"customer":{"avg_amount":703.28,"tx_count_24h":5,"known_merchants":["MERC-003"]},"merchant":{"id":"MERC-512","mcc":"5814","avg_amount":480.5},"terminal":{"is_online":true,"card_present":false,"km_from_home":799.4966241306},"last_transaction":{"timestamp":"2026-03-13T05:37:37Z","km_from_current":793.7849846257}}'

echo "============================================"
echo "  Rinha de Backend 2026 - Benchmark"
echo "============================================"
echo ""
echo "  Target:      ${BASE_URL}"
echo "  Requests:    ${TOTAL}"
echo "  Concurrency: ${CONCURRENCY}"
echo ""

# 1. Wait for /ready
echo "  [.] Checking /ready..."
i=1; while [ "$i" -le 60 ]; do
  if curl -fsS --max-time 2 "$READY_URL" >/dev/null 2>&1; then
    echo "  [+] Service ready"; break
  fi
  if [ "$i" -eq 60 ]; then echo "  [X] Not ready" >&2; exit 1; fi
  sleep 1; i=$((i + 1))
done
echo ""

# 2. Write payloads to temp files
PAYLOAD_FILE_A=$(mktemp); PAYLOAD_FILE_B=$(mktemp)
PAYLOAD_FILE_C=$(mktemp); PAYLOAD_FILE_D=$(mktemp)
trap 'rm -f $PAYLOAD_FILE_A $PAYLOAD_FILE_B $PAYLOAD_FILE_C $PAYLOAD_FILE_D' EXIT
printf '%s' "$PAYLOAD_A" > "$PAYLOAD_FILE_A"
printf '%s' "$PAYLOAD_B" > "$PAYLOAD_FILE_B"
printf '%s' "$PAYLOAD_C" > "$PAYLOAD_FILE_C"
printf '%s' "$PAYLOAD_D" > "$PAYLOAD_FILE_D"

ab_run() {
  local label="$1" payload="$2" count="$3"
  echo "  --- ${label} ---"
  echo "  Reqs: ${count} @ concurrency ${CONCURRENCY}"
  AB_OUT=$(ab -p "$payload" -T 'application/json' -n "$count" -c "$CONCURRENCY" -k -q "$SCORE_URL" 2>&1)
  REQ_PER_SEC=$(echo "$AB_OUT" | grep "Requests per second" | awk '{print $4}')
  TIME_PER_REQ=$(echo "$AB_OUT" | grep "Time per request" | head -1 | awk '{print $4}')
  FAILED=$(echo "$AB_OUT" | grep "Failed requests" | awk '{print $3}')
  NON_2XX=$(echo "$AB_OUT" | grep "Non-2xx responses" | awk '{print $3}')
  P99=$(echo "$AB_OUT" | grep "^  99%" | awk '{print $2}')
  echo "  Req/s:  ${REQ_PER_SEC:-N/A}     Time: ${TIME_PER_REQ:-N/A} ms"
  echo "  Failed: ${FAILED:-0}     Non-2xx: ${NON_2XX:-0}     p99: ${P99:-N/A} ms"
  echo ""
}

ab_run "Round 1/4 (in_person, card, last_tx)" "$PAYLOAD_FILE_A" "$((TOTAL / 4))"
ab_run "Round 2/4 (online, no_card, last_tx)" "$PAYLOAD_FILE_B" "$((TOTAL / 4))"
ab_run "Round 3/4 (in_person, card, no_last_tx)" "$PAYLOAD_FILE_C" "$((TOTAL / 4))"
ab_run "Round 4/4 (online, no_card, large_km)" "$PAYLOAD_FILE_D" "$((TOTAL / 4))"

echo "============================================"
echo "  Benchmark Complete"
echo "============================================"
echo "  ${TOTAL} requests in 4 rounds, concurrency=${CONCURRENCY}"
echo "  Official test: 54,100 reqs, ramp-up, p99 cutoff at 2000ms"