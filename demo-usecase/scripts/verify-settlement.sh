#!/usr/bin/env bash
# Settlement acceptance checks (PRD §13 "Settlement", C-2.x) and README constraint 4.
#
# These mutate data and drive the worker deliberately, so they are separate from
# verify.sh. The interval worker is stopped for the duration and restarted at the end.
set -uo pipefail

cd "$(dirname "$0")/.."
set -a; . ./.env; set +a

API="http://localhost:${COLLECTIONS_PUBLISH_PORT:-8088}"
PSQL=(docker compose exec -T -e PGPASSWORD="$POSTGRES_PASSWORD" postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tA)

PASS=0; FAIL=0
ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n'  "$1"; printf '        %s\n' "${2:-}"; FAIL=$((FAIL+1)); }
head2(){ printf '\n\033[1m%s\033[0m\n' "$1"; }
q()    { "${PSQL[@]}" -c "$1" | tr -d '[:space:]'; }   # numbers and ids
qt()   { "${PSQL[@]}" -c "$1" | tr -d '\r' | head -1; }  # free text - keep the spaces

token() {
  python3 -c "import base64,json,sys;print('dev.'+base64.urlsafe_b64encode(json.dumps({'sub':sys.argv[1],'merchantId':sys.argv[2],'role':sys.argv[3]}).encode()).decode().rstrip('='))" "$1" "$2" "$3"
}

# Create a cleared collection for a merchant and return its id.
make_cleared() { # merchantId amountMinor
  local t id
  t=$(token "svc_$1" "$1" service)
  id=$(curl -s -X POST "$API/v1/collections" -H "Authorization: Bearer $t" \
        -H 'Content-Type: application/json' \
        -d "{\"channel\":\"mpesa\",\"amountMinor\":$2,\"customerReference\":\"cust_stl\",\"merchantReference\":\"stl-$1-$RANDOM-$RANDOM\"}" \
      | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",""))' 2>/dev/null)
  curl -s -o /dev/null -X POST "$API/v1/channel-callbacks" -H 'Content-Type: application/json' \
       -d "{\"collectionId\":\"$id\",\"outcome\":\"cleared\"}"
  echo "$id"
}

run_worker() { # extra env...
  docker compose run --rm -T -e RUN_MODE=once "$@" settlement-worker 2>&1
}

echo "stopping the interval worker so these checks are deterministic"
docker compose stop settlement-worker >/dev/null 2>&1

head2 "Settlement (PRD §13)"

# --- C-2.1 a nightly run settles the day's cleared collections -----------------
A1=$(make_cleared mch_001 120000)
A2=$(make_cleared mch_003 340000)
BEFORE_CLEARED=$(q "SELECT COUNT(*) FROM collections c JOIN merchants m ON m.id=c.merchant_id WHERE c.status='cleared' AND m.data_region='KE'")
OUT=$(run_worker)
AFTER_CLEARED=$(q "SELECT COUNT(*) FROM collections c JOIN merchants m ON m.id=c.merchant_id WHERE c.status='cleared' AND m.data_region='KE'")
S1=$(q "SELECT status FROM collections WHERE id='$A1'")
if [[ "$S1" == "settled" && "$AFTER_CLEARED" -lt "$BEFORE_CLEARED" ]]; then
  ok "C-2.1 nightly run settled the cleared set (cleared $BEFORE_CLEARED -> $AFTER_CLEARED); $A1 is now 'settled'"
else
  bad "C-2.1 nightly settlement" "status=$S1 before=$BEFORE_CLEARED after=$AFTER_CLEARED"
fi

# Balancing entries exist for the settlement itself
LINE=$(q "SELECT settlement_line_id FROM collections WHERE id='$A1'")
FEE=$(q "SELECT COUNT(*) FROM ledger_entries WHERE event_type='fee' AND event_id='$LINE'")
STL=$(q "SELECT COUNT(*) FROM ledger_entries WHERE event_type='settlement' AND event_id='$LINE'")
if [[ "$FEE" == "2" && "$STL" == "2" ]]; then
  ok "C-3.1 settlement line $LINE produced balancing fee and settlement entries (2 + 2)"
else
  bad "C-3.1 settlement ledger entries" "fee=$FEE settlement=$STL for line $LINE"
fi

# C-2.4 one payout instruction per merchant per run
PAY=$(q "SELECT COUNT(*) FROM payout_instructions WHERE settlement_line_id='$LINE'")
[[ "$PAY" == "1" ]] && ok "C-2.4 exactly one payout instruction emitted for line $LINE" \
  || bad "C-2.4 payout instruction" "count=$PAY"

# --- C-2.7 re-running a settled period must not double-settle ------------------
LINES_BEFORE=$(q "SELECT COUNT(*) FROM settlement_lines")
NET_BEFORE=$(q "SELECT COALESCE(SUM(net_minor),0) FROM settlement_lines")
run_worker >/dev/null 2>&1
LINES_AFTER=$(q "SELECT COUNT(*) FROM settlement_lines")
NET_AFTER=$(q "SELECT COALESCE(SUM(net_minor),0) FROM settlement_lines")
DOUBLE=$(q "SELECT COUNT(*) FROM collections WHERE settlement_line_id IS NOT NULL AND status <> 'settled'")
if [[ "$NET_BEFORE" == "$NET_AFTER" && "$DOUBLE" == "0" ]]; then
  ok "C-2.7 re-run settled nothing further (net unchanged at $NET_AFTER, lines $LINES_BEFORE -> $LINES_AFTER)"
else
  bad "C-2.7 idempotency" "net $NET_BEFORE -> $NET_AFTER, lines $LINES_BEFORE -> $LINES_AFTER"
fi

# --- C-2.6 a failure affecting one merchant leaves no other half-written -------
# Merchants are processed in sorted order, so failing at mch_003 means mch_001 has
# already committed and mch_003 must be entirely untouched.
B1=$(make_cleared mch_001 55000)
B3=$(make_cleared mch_003 77000)
FOUT=$(run_worker -e SETTLEMENT_FAIL_AT_MERCHANT=mch_003)
S_B1=$(q "SELECT status FROM collections WHERE id='$B1'")
S_B3=$(q "SELECT status FROM collections WHERE id='$B3'")
FAILED_RUN=$(q "SELECT id FROM settlement_runs WHERE status='failed' ORDER BY started_at DESC LIMIT 1")
ORPHAN=$(q "SELECT COUNT(*) FROM settlement_lines WHERE run_id='$FAILED_RUN' AND merchant_id='mch_003'")
if [[ "$S_B1" == "settled" && "$S_B3" == "cleared" && "$ORPHAN" == "0" ]]; then
  ok "C-2.6 induced failure at mch_003: mch_001 fully settled, mch_003 untouched (still 'cleared', no line)"
else
  bad "C-2.6 per-merchant atomicity" "mch_001=$S_B1 mch_003=$S_B3 orphan_lines=$ORPHAN"
fi

# C-2.5 the failure reason is persisted and names the run, merchant, row and cause
REASON=$(qt "SELECT failure_reason FROM settlement_runs WHERE id='$FAILED_RUN'")
if grep -q 'mch_003' <<<"$REASON" && grep -qi 'row' <<<"$REASON"; then
  ok "C-2.5 failure reason persisted and specific: \"${REASON:0:96}...\""
else
  bad "C-2.5 failure reason" "$REASON"
fi

# Clean up the induced-failure leftovers so the stack returns to a settled state.
run_worker >/dev/null 2>&1

# --- C-2.2 month-end ----------------------------------------------------------
MOUT=$(run_worker -e FORCE_MONTH_END=true)
if grep -q 'month-end reconciliation complete' <<<"$MOUT"; then
  STMTS=$(grep -c 'monthly statement produced' <<<"$MOUT")
  ok "C-2.2 month-end run reconciled the full month and produced $STMTS merchant statements"
else
  bad "C-2.2 month-end run" "$(tail -3 <<<"$MOUT")"
fi

head2 "Constraint 4 - lag is emitted with the worker stopped"

# The worker is still stopped here. That is the point: the signal that tells you the
# worker is dead must not be produced by the worker.
WSTATE=$(docker compose ps --format '{{.Name}} {{.State}}' | grep settlement-worker || echo "settlement-worker absent")

# There must be unsettled work for lag to be non-zero. The preceding checks deliberately
# settle everything, so create some - otherwise this check compares 0 with 0 and fails for
# the wrong reason.
LAGID=$(make_cleared mch_001 99000)
sleep 17   # longer than LAG_REFRESH_INTERVAL so the gauge is recomputed at least once
L1=$(curl -s "$API/metrics" | grep '^mopay_settlement_lag_seconds{' | awk '{print $2}')
sleep 17
L2=$(curl -s "$API/metrics" | grep '^mopay_settlement_lag_seconds{' | awk '{print $2}')
# Lag must be non-zero AND climbing, with nothing settling it.
CLIMBED=$(python3 -c "
try:
    a,b=float('$L1'),float('$L2')
    print('yes' if b>a and b>0 else 'no')
except Exception:
    print('no')" 2>/dev/null)
if [[ "$CLIMBED" == "yes" ]]; then
  ok "constraint 4 lag kept climbing with no worker running ($WSTATE): ${L1%.*}s -> ${L2%.*}s"
else
  bad "constraint 4 continuous lag" "worker=$WSTATE l1=$L1 l2=$L2 (collection $LAGID)"
fi

STATUS=$(curl -s "$API/v1/settlement-status" -H "Authorization: Bearer $(token u_ops '' operations)")
LASTRUN=$(echo "$STATUS" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("lastCompletedRunAt",""))' 2>/dev/null)
[[ -n "$LASTRUN" ]] && ok "S-3.4 settlement-status reports the last successful run time ($LASTRUN)" \
  || bad "S-3.4 last run time" "$STATUS"

echo
echo "restarting the interval worker"
docker compose start settlement-worker >/dev/null 2>&1

printf '\n\033[1mResult:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
