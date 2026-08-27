#!/usr/bin/env bash
#
# run_benzhi_smoke.sh — build and smoke-test the thermal-vacuum-test-gate
# service against a real local HTTP endpoint. It starts the server, probes its
# health and plan-lock API behaviour, then tears everything down. No external
# network access is required.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

WORK="$(mktemp -d)"
BIN="$WORK/thermal-vacuum-test-gate"
DB="$WORK/gate.db"
LOG="$WORK/server.log"
PORT="${TVTG_PORT:-18080}"
SERVER_PID=""

cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "==> building server binary"
go build -o "$BIN" ./cmd/server

echo "==> starting server on 127.0.0.1:$PORT"
TVTG_ADDR="127.0.0.1:$PORT" TVTG_DB="file:$DB" "$BIN" >"$LOG" 2>&1 &
SERVER_PID=$!

# Wait for the server to become healthy.
for _ in $(seq 1 100); do
  if curl -sS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

HEALTH="$(curl -sS "http://127.0.0.1:$PORT/healthz" 2>/dev/null || true)"
if [[ "$HEALTH" != *'"status":"ok"'* ]]; then
  echo "health probe failed: $HEALTH" >&2
  exit 1
fi
echo "==> health OK"

PLAN_JSON='{"id":"smoke-plan","specimen_id":"spec-smoke","calibration_summary":"cal-2026","cycles":1,"sensors":[{"id":"s1","group":"g1","collector_id":"c1"}],"stages":[{"name":"evacuate","sequence":1},{"name":"cold_soak","sequence":2,"dependencies":["evacuate"]},{"name":"hot_soak","sequence":3,"dependencies":["cold_soak"]}]}'

LOCK="$(curl -sS -X POST "http://127.0.0.1:$PORT/v1/plans/lock" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: smoke-plan' \
  -d "$PLAN_JSON" 2>/dev/null || true)"
if [[ "$LOCK" != *'"version":1'* ]]; then
  echo "plan lock failed: $LOCK" >&2
  exit 1
fi
echo "==> plan lock OK"

GET="$(curl -sS "http://127.0.0.1:$PORT/v1/plans/smoke-plan" 2>/dev/null || true)"
if [[ "$GET" != *'"id":"smoke-plan"'* ]]; then
  echo "plan get failed: $GET" >&2
  exit 1
fi
echo "==> plan read-back OK"

echo "==> smoke test passed"
