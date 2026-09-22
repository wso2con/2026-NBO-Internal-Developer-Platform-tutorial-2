#!/usr/bin/env bash
# Drive peak-hour load at a DEPLOYED mopay environment, from this machine.
#
# The Compose path (`make load`) runs the generator inside the stack. On the platform there is
# nothing to run it inside: `collections-api` is the only component with an external endpoint, and
# every environment publishes it on the shared gateway. So the laptop is the load generator and
# the gateway is the door.
#
#   ./scripts/load-platform.sh                        # development, default settings
#   ./scripts/load-platform.sh prod-ke
#   ./scripts/load-platform.sh development --port-forward
#   CONCURRENCY=40 DURATION=15s ./scripts/load-platform.sh
#
# Every knob is an environment variable; the ones below are measured values for a database with
# 27 usable connections.
#
# GATEWAY_HOST has no sensible default - it is your own OpenChoreo installation's external
# gateway hostname. Set it in the environment, or export LOADGEN_BASE_URL to bypass the
# derivation entirely.
#
# Needs: Go (the generator is `demo-usecase/loadgen`), and kubectl only for --port-forward.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ENVIRONMENT="${1:-development}"
[ "${1:-}" = "--port-forward" ] && ENVIRONMENT=development
PORT_FORWARD=false
for arg in "$@"; do [ "$arg" = "--port-forward" ] && PORT_FORWARD=true; done

# --- what to send -----------------------------------------------------------------------------
# 130 sustains the failure at 27 usable connections. Against Compose the same failure needs only
# about 80 - throughput over a gateway is about a third of it. Raise this, not the duration, if the
# incident does not reproduce.
CONCURRENCY="${CONCURRENCY:-130}"
DURATION="${DURATION:-40s}"
CHANNEL="${CHANNEL:-mpesa}"
# Seeded merchants. C-1.5 refuses a collection once a merchant's unsettled balance would pass its
# float limit, so spread the load and keep the amounts small enough to absorb between runs.
MERCHANT_IDS="${MERCHANT_IDS:-mch_001,mch_002,mch_003,mch_005,mch_006}"
AMOUNT_MIN_MINOR="${AMOUNT_MIN_MINOR:-1000}"
AMOUNT_MAX_MINOR="${AMOUNT_MAX_MINOR:-20000}"
CLEAR_RATIO="${CLEAR_RATIO:-1.0}"

# --- where to send it -------------------------------------------------------------------------
OC_NAMESPACE="${OC_NAMESPACE:-default}"
GATEWAY_HOST="${GATEWAY_HOST:-}"
PF_PORT="${PF_PORT:-18080}"
pf_pid=""

if [ "$PORT_FORWARD" = true ]; then
    # Straight at the Service, bypassing the gateway. Needs data-plane access, which is a platform
    # engineer's credential, not a developer's - but it removes the gateway from the measurement.
    cell_ns=$(kubectl get ns -o name \
        | sed 's|^namespace/||' \
        | grep -E "^dp-${OC_NAMESPACE}-mopay-${ENVIRONMENT}-" \
        | head -1)
    [ -n "$cell_ns" ] || { echo "no cell namespace matching dp-${OC_NAMESPACE}-mopay-${ENVIRONMENT}-*" >&2; exit 1; }

    echo "port-forwarding svc/collections-api in $cell_ns -> localhost:$PF_PORT"
    kubectl port-forward -n "$cell_ns" svc/collections-api "$PF_PORT:8080" >/dev/null 2>&1 &
    pf_pid=$!
    trap '[ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true' EXIT
    # The forward needs a moment before the first connection, or every early request fails.
    for _ in $(seq 1 20); do
        curl -fsS "http://localhost:$PF_PORT/healthz" >/dev/null 2>&1 && break
        sleep 0.5
    done
    BASE_URL="http://localhost:$PF_PORT"
else
    # /{component}-{endpoint} is how every external endpoint is published, and one hostname serves
    # the whole environment.
    if [ -z "${LOADGEN_BASE_URL:-}" ] && [ -z "$GATEWAY_HOST" ]; then
        echo "set GATEWAY_HOST to your OpenChoreo gateway hostname, or LOADGEN_BASE_URL to the" >&2
        echo "full collections-api address. Neither is set." >&2
        exit 2
    fi
    BASE_URL="${LOADGEN_BASE_URL:-https://${ENVIRONMENT}-${OC_NAMESPACE}.${GATEWAY_HOST}/collections-api-api}"
fi

# --- go ------------------------------------------------------------------------------------
# Record both ends. Alerts arrive ~5 minutes after the load, so attributing them needs a bounded
# window, and "about ten past" is not a window.
started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "load starting   $started"
echo "  target        $BASE_URL"
echo "  concurrency   $CONCURRENCY for $DURATION"
echo "  merchants     $MERCHANT_IDS"
echo

set +e
# `loadgen` is its own Go module, so the build has to run from inside it.
(
    cd loadgen
    LOADGEN_BASE_URL="$BASE_URL" \
    LOADGEN_CHANNEL="$CHANNEL" \
    LOADGEN_MERCHANT_IDS="$MERCHANT_IDS" \
    LOADGEN_CONCURRENCY="$CONCURRENCY" \
    LOADGEN_DURATION="$DURATION" \
    LOADGEN_AMOUNT_MIN_MINOR="$AMOUNT_MIN_MINOR" \
    LOADGEN_AMOUNT_MAX_MINOR="$AMOUNT_MAX_MINOR" \
    LOADGEN_CLEAR_RATIO="$CLEAR_RATIO" \
        go run ./cmd/loadgen
)
rc=$?
set -e

ended=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo
echo "load finished   $ended   (exit $rc)"
cat <<NOTE

Window for querying alerts, logs and metrics:
  start_time: $started
  end_time:   $ended

Alerts do not appear for ~5 minutes after the load. Keep talking; do not stand watching.

If this was the connection-exhaustion incident, two things follow:
  - recover the pool:  update_release_binding ledger-$ENVIRONMENT  dbMaxConns: 10
  - RESEED $ENVIRONMENT. The incident strands collections - cleared, with no ledger entries,
    44 of them after one run - and a clean baseline is impossible until you do.
NOTE
exit $rc
