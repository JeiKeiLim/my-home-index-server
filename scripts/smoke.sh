#!/usr/bin/env bash
# scripts/smoke.sh — harness §Smoke (Stage 1.5).
#
# Builds the binary into ./port-manager, starts it on a free high port
# with AUTH_TOKEN=smoketest, hits /healthz (unauthenticated) and
# /ports.json (bearer-auth'd), then sends SIGTERM and asserts a clean
# exit (status 0 or 143). Any failure aborts with a non-zero exit code
# so make / CI see it.
#
# Usage:
#   ./scripts/smoke.sh                  # builds, runs, verifies, exits 0
#   SMOKE_PORT=40077 ./scripts/smoke.sh # override listen port
#
# Environment:
#   SMOKE_PORT     listen port (default 40010)
#   SMOKE_TOKEN    AUTH_TOKEN value     (default smoketest)
#   SMOKE_SECRET   SESSION_SECRET value (default smoke-secret-0123456789abcdef0123)
#   SMOKE_TIMEOUT  seconds to wait for /healthz to come up (default 10)

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SMOKE_PORT="${SMOKE_PORT:-40010}"
SMOKE_TOKEN="${SMOKE_TOKEN:-smoketest-0123456789abcdef}"
SMOKE_SECRET="${SMOKE_SECRET:-smoke-secret-0123456789abcdef0123}"
SMOKE_TIMEOUT="${SMOKE_TIMEOUT:-10}"
BINARY="./port-manager"
PORT_MANAGER_BIN="$BINARY"
PIDFILE=".smoke.pid"
LOGFILE=".smoke.log"

log()  { printf '[smoke] %s\n' "$*" >&2; }
fail() { printf '[smoke] FAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
  if [[ -f "$PIDFILE" ]]; then
    local pid
    pid="$(cat "$PIDFILE" 2>/dev/null || true)"
    if [[ -n "${pid:-}" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
    rm -f "$PIDFILE"
  fi
}
trap cleanup EXIT

log "building $BINARY"
# Use the Makefile so the version string is baked in via -ldflags. The
# --version assertion below depends on that ldflag being applied.
make build >/dev/null

# Assert that --version reports something other than the default "dev"
# sentinel — this catches a regression where the Makefile's
# `-ldflags -X main.version=…` is dropped and the binary silently falls
# back to the hard-coded default string.
got="$("$PORT_MANAGER_BIN" --version)"
if [ "$got" = "port-manager dev" ]; then
    echo "smoke: --version returned default 'port-manager dev' — -ldflags main.version was not applied" >&2
    exit 1
fi
echo "smoke: --version reports $got"

log "starting $BINARY on :$SMOKE_PORT"
AUTH_TOKEN="$SMOKE_TOKEN" \
SESSION_SECRET="$SMOKE_SECRET" \
PORT="$SMOKE_PORT" \
PUBLIC_HOST="localhost" \
"$BINARY" --port "$SMOKE_PORT" --public-host localhost >"$LOGFILE" 2>&1 &
PID=$!
echo "$PID" >"$PIDFILE"

# Wait for /healthz to come up (binary takes <1s in practice; allow a budget).
deadline=$(( $(date +%s) + SMOKE_TIMEOUT ))
healthy=0
while (( $(date +%s) < deadline )); do
  if ! kill -0 "$PID" 2>/dev/null; then
    log "binary exited early — see $LOGFILE"
    sed 's/^/[smoke:log] /' "$LOGFILE" >&2 || true
    fail "process $PID died before /healthz responded"
  fi
  if curl -sf "http://localhost:${SMOKE_PORT}/healthz" >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 0.2
done

if (( healthy != 1 )); then
  sed 's/^/[smoke:log] /' "$LOGFILE" >&2 || true
  fail "/healthz did not respond within ${SMOKE_TIMEOUT}s"
fi

body="$(curl -sf "http://localhost:${SMOKE_PORT}/healthz")"
if [[ "$body" != "ok" ]]; then
  fail "/healthz returned unexpected body: $body"
fi
log "/healthz OK"

ports_body="$(curl -sf -H "Authorization: Bearer ${SMOKE_TOKEN}" \
  "http://localhost:${SMOKE_PORT}/ports.json")" \
  || fail "/ports.json request failed"
case "$ports_body" in
  '['*) log "/ports.json OK (returned a JSON array)" ;;
  *) fail "/ports.json did not return a JSON array: ${ports_body:0:120}" ;;
esac

log "sending SIGTERM to $PID"
kill -TERM "$PID" 2>/dev/null || true

# wait returns the child's exit status; tolerate SIGTERM (143) and 0.
status=0
wait "$PID" 2>/dev/null || status=$?
case "$status" in
  0|143) log "binary exited cleanly (status=$status)" ;;
  *) fail "binary exited with unexpected status=$status" ;;
esac

rm -f "$PIDFILE" "$LOGFILE"
log "smoke check passed"
