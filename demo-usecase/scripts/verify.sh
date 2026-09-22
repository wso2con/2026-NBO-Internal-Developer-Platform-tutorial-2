#!/usr/bin/env bash
# Acceptance checks for mopay, against the running Compose stack (PRD §13).
#
# Each check names the requirement it proves. Run with `make verify`.
set -uo pipefail

cd "$(dirname "$0")/.."
set -a; . ./.env; set +a

API="http://localhost:${COLLECTIONS_PUBLISH_PORT:-8088}"
# Read the network off a running container rather than guessing at the project name.
NET="$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$(docker compose ps -q ledger)")"
LEDGER="http://ledger:8080"
PSQL_OWNER=(docker compose exec -T -e PGPASSWORD="$POSTGRES_PASSWORD" postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tA)
PSQL_APP=(docker compose exec -T -e PGPASSWORD="$APP_DB_PASSWORD" postgres psql -U "$APP_DB_USER" -h 127.0.0.1 -d "$POSTGRES_DB" -tA)

PASS=0; FAIL=0
ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n'  "$1"; printf '        %s\n' "${2:-}"; FAIL=$((FAIL+1)); }
head2(){ printf '\n\033[1m%s\033[0m\n' "$1"; }

token() { # subject merchantId role
  python3 -c "import base64,json,sys;print('dev.'+base64.urlsafe_b64encode(json.dumps({'sub':sys.argv[1],'merchantId':sys.argv[2],'role':sys.argv[3]}).encode()).decode().rstrip('='))" "$1" "$2" "$3"
}
# curl inside the compose network, for the internal-only ledger.
icurl() { docker run --rm --network "$NET" curlimages/curl:latest -s "$@"; }

MCH_A=mch_001
MCH_B=mch_002
NEAR=mch_004          # seeded deliberately close to its float limit (PRD §12)
T_A=$(token user_a "$MCH_A" merchant)
T_B=$(token user_b "$MCH_B" merchant)
T_NEAR=$(token user_n "$NEAR" merchant)
T_OPS=$(token user_o "" operations)

head2 "Collections (PRD §13)"

# C-1.3 idempotency
REF="verify-$(date +%s)-$RANDOM"
R1=$(curl -s -X POST "$API/v1/collections" -H "Authorization: Bearer $T_A" \
      -H 'Content-Type: application/json' \
      -d "{\"channel\":\"mpesa\",\"amountMinor\":150000,\"customerReference\":\"cust_ver_1\",\"merchantReference\":\"$REF\"}")
ID1=$(echo "$R1" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",""))' 2>/dev/null)
R2=$(curl -s -X POST "$API/v1/collections" -H "Authorization: Bearer $T_A" \
      -H 'Content-Type: application/json' \
      -d "{\"channel\":\"mpesa\",\"amountMinor\":150000,\"customerReference\":\"cust_ver_1\",\"merchantReference\":\"$REF\"}")
ID2=$(echo "$R2" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",""))' 2>/dev/null)
CNT=$("${PSQL_OWNER[@]}" -c "SELECT COUNT(*) FROM collections WHERE merchant_reference='$REF'" | tr -d '[:space:]')
if [[ -n "$ID1" && "$ID1" == "$ID2" && "$CNT" == "1" ]]; then
  ok "C-1.3 duplicate merchantReference returns the same id ($ID1) and creates one row"
else
  bad "C-1.3 idempotency" "id1=$ID1 id2=$ID2 rows=$CNT resp=$R2"
fi

# C-1.8 currency derived from the channel, never the caller
CUR=$(echo "$R1" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("currency",""))' 2>/dev/null)
CTRY=$(echo "$R1" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("originCountry",""))' 2>/dev/null)
REF2="verify-cur-$(date +%s)-$RANDOM"
R3=$(curl -s -X POST "$API/v1/collections" -H "Authorization: Bearer $T_A" \
      -H 'Content-Type: application/json' \
      -d "{\"channel\":\"mpesa\",\"currency\":\"NGN\",\"amountMinor\":1000,\"merchantReference\":\"$REF2\"}")
CUR2=$(echo "$R3" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("currency",""))' 2>/dev/null)
if [[ "$CUR" == "KES" && "$CTRY" == "KE" && "$CUR2" == "KES" ]]; then
  ok "C-1.8 mpesa yields KES/KE, and a caller-supplied currency=NGN is ignored"
else
  bad "C-1.8 currency derivation" "cur=$CUR country=$CTRY spoofed=$CUR2"
fi

# C-1.2 unsupported channel refused (SCOPE.md caps us at two channels)
CODE=$(curl -s -X POST "$API/v1/collections" -H "Authorization: Bearer $T_A" \
        -H 'Content-Type: application/json' \
        -d '{"channel":"paystack_card","amountMinor":1000,"merchantReference":"x-'$RANDOM'"}' \
      | python3 -c 'import sys,json;print(json.load(sys.stdin).get("code",""))' 2>/dev/null)
[[ "$CODE" == "unsupported_channel" ]] && ok "C-1.2 unsupported channel refused with code $CODE" \
  || bad "C-1.2 unsupported channel" "code=$CODE"

# C-1.5 float limit, with balance and limit in the body
BODY=$(curl -s -X POST "$API/v1/collections" -H "Authorization: Bearer $T_NEAR" \
        -H 'Content-Type: application/json' \
        -d '{"channel":"mpesa","amountMinor":90000000,"merchantReference":"float-'$RANDOM'"}')
FCODE=$(echo "$BODY" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("code",""))' 2>/dev/null)
FBAL=$(echo "$BODY" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("details",{}).get("unsettledBalanceMinor",""))' 2>/dev/null)
FLIM=$(echo "$BODY" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("details",{}).get("floatLimitMinor",""))' 2>/dev/null)
if [[ "$FCODE" == "float_limit_exceeded" && -n "$FBAL" && -n "$FLIM" ]]; then
  ok "C-1.5 float limit refused with balance=$FBAL limit=$FLIM"
else
  bad "C-1.5 float limit" "$BODY"
fi

# C-1.7 callback clears, replay changes nothing
CB1=$(curl -s -X POST "$API/v1/channel-callbacks" -H 'Content-Type: application/json' \
      -d "{\"collectionId\":\"$ID1\",\"outcome\":\"cleared\"}")
CH1=$(echo "$CB1" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("changed"))' 2>/dev/null)
CB2=$(curl -s -X POST "$API/v1/channel-callbacks" -H 'Content-Type: application/json' \
      -d "{\"collectionId\":\"$ID1\",\"outcome\":\"cleared\"}")
CH2=$(echo "$CB2" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("changed"))' 2>/dev/null)
ENTRIES=$("${PSQL_OWNER[@]}" -c "SELECT COUNT(*) FROM ledger_entries WHERE event_id='$ID1'" | tr -d '[:space:]')
if [[ "$CH1" == "True" && "$CH2" == "False" && "$ENTRIES" == "2" ]]; then
  ok "C-1.7 callback cleared the collection; replay changed nothing; exactly 2 ledger entries"
else
  bad "C-1.7 callback idempotency" "changed1=$CH1 changed2=$CH2 entries=$ENTRIES"
fi

# A cleared collection must never exist without its ledger entries - money taken but not
# recorded - because settlement would then pay out against a credit that was never
# written.
#
# This got STRONGER when `ledger` became the only component holding a database
# connection. collections-api used to own the status update on its own pool, so with the
# ledger unreachable it could commit `cleared` while the ledger write failed, leaving
# exactly that gap. Now both go through the same component: if the ledger is down the
# callback fails whole and the collection stays `pending`, so the gap is never created.
#
# The gap is narrowed, not eliminated - the status update and the entry write are still
# two calls, so a failure between them can still strand a collection. The repair path
# therefore still matters: channels retry, and the retry must heal it.
REF3="verify-heal-$(date +%s)-$RANDOM"
HID=$(curl -s -X POST "$API/v1/collections" -H "Authorization: Bearer $T_A" \
      -H 'Content-Type: application/json' \
      -d "{\"channel\":\"mpesa\",\"amountMinor\":12300,\"merchantReference\":\"$REF3\"}" \
      | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",""))' 2>/dev/null)
docker compose stop ledger >/dev/null 2>&1
DOWN=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/v1/channel-callbacks" \
        -H 'Content-Type: application/json' -d "{\"collectionId\":\"$HID\",\"outcome\":\"cleared\"}")
GAP=$("${PSQL_OWNER[@]}" -c "SELECT COUNT(*) FROM ledger_entries WHERE event_id='$HID'" | tr -d '[:space:]')
# The collection must NOT have been marked cleared: no gap state exists to repair.
DOWNSTATUS=$("${PSQL_OWNER[@]}" -c "SELECT status FROM collections WHERE id='$HID'" | tr -d '[:space:]')
docker compose start ledger >/dev/null 2>&1
for _ in $(seq 1 20); do icurl "$LEDGER/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -s -o /dev/null -X POST "$API/v1/channel-callbacks" \
     -H 'Content-Type: application/json' -d "{\"collectionId\":\"$HID\",\"outcome\":\"cleared\"}"
HEALED=$("${PSQL_OWNER[@]}" -c "SELECT COUNT(*) FROM ledger_entries WHERE event_id='$HID'" | tr -d '[:space:]')
if [[ "$DOWN" == "503" && "$GAP" == "0" && "$DOWNSTATUS" == "pending" && "$HEALED" == "2" ]]; then
  ok "ledger down: callback refused ($DOWN), collection left 'pending' with no entries; the retry then cleared it and wrote 2"
else
  bad "cleared-without-ledger-entries repair" \
      "callback=$DOWN gap_entries=$GAP status_while_down=$DOWNSTATUS after_retry=$HEALED"
fi

head2 "Ledger (PRD §13)"

# C-3.5 sum-zero rejection, via the internal-only ledger
UNBAL=$(icurl -X POST "$LEDGER/internal/ledger/entries" -H 'Content-Type: application/json' \
  -d "{\"transactionGroupId\":\"tg_verify_$RANDOM\",\"eventType\":\"collection\",\"eventId\":\"col_x\",\"entries\":[
      {\"merchantId\":\"$MCH_A\",\"direction\":\"credit\",\"amountMinor\":25000,\"currency\":\"KES\"},
      {\"merchantId\":\"mopay_house\",\"direction\":\"debit\",\"amountMinor\":20000,\"currency\":\"KES\"}]}")
UCODE=$(echo "$UNBAL" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("code",""))' 2>/dev/null)
USUM=$(echo "$UNBAL" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("details",{}).get("sumMinor",""))' 2>/dev/null)
if [[ "$UCODE" == "ledger_not_balanced" && "$USUM" == "5000" ]]; then
  ok "C-3.5 unbalanced write rejected, error carries the actual sum ($USUM minor)"
else
  bad "C-3.5 sum-zero" "$UNBAL"
fi

# C-3.3 append-only, enforced by Postgres against the APPLICATION role
UPD=$("${PSQL_APP[@]}" -c "UPDATE ledger_entries SET amount_minor = 1 WHERE id = (SELECT id FROM ledger_entries LIMIT 1)" 2>&1)
DEL=$("${PSQL_APP[@]}" -c "DELETE FROM ledger_entries WHERE id = (SELECT id FROM ledger_entries LIMIT 1)" 2>&1)
if grep -qi 'permission denied' <<<"$UPD" && grep -qi 'permission denied' <<<"$DEL"; then
  ok "C-3.3 direct UPDATE and DELETE on ledger_entries refused by Postgres for the app role"
else
  bad "C-3.3 append-only" "update=$UPD delete=$DEL"
fi
SLU=$("${PSQL_APP[@]}" -c "UPDATE settlement_lines SET net_minor = 0 WHERE id = (SELECT id FROM settlement_lines LIMIT 1)" 2>&1)
grep -qi 'permission denied' <<<"$SLU" \
  && ok "C-2.3 settlement_lines are equally immutable to the app role" \
  || bad "C-2.3 settlement line immutability" "$SLU"

# C-3.2 balance as of a past timestamp
NOWBAL=$(icurl "$LEDGER/internal/ledger/balances/$MCH_A" | python3 -c 'import sys,json;print(json.load(sys.stdin)["amountMinor"])' 2>/dev/null)
PASTBAL=$(icurl "$LEDGER/internal/ledger/balances/$MCH_A?asOf=2026-01-01T00:00:00Z" | python3 -c 'import sys,json;print(json.load(sys.stdin)["amountMinor"])' 2>/dev/null)
if [[ -n "$NOWBAL" && "$PASTBAL" == "0" ]]; then
  ok "C-3.2 balance now=$NOWBAL, as-of 2026-01-01 (before any data) = 0"
else
  bad "C-3.2 as-of balance" "now=$NOWBAL past=$PASTBAL"
fi

# Every transaction group balances, across the whole seeded dataset
BADG=$("${PSQL_OWNER[@]}" -c "SELECT COUNT(*) FROM (SELECT transaction_group_id FROM ledger_entries GROUP BY transaction_group_id HAVING SUM(CASE WHEN direction='credit' THEN amount_minor ELSE -amount_minor END) <> 0) t" | tr -d '[:space:]')
[[ "$BADG" == "0" ]] && ok "C-3.5 every transaction group in the datastore sums to zero" \
  || bad "C-3.5 dataset balance" "$BADG unbalanced groups"

# A statement's headline totals must equal the sum of its own per-channel breakdown.
# These are computed by two different queries, and an earlier version filtered them on
# different bases (run period vs collection settled_at), so a month-boundary run made the
# statement disagree with itself.
MONTH=$(date -u +%Y-%m)
STM=$(curl -s "$API/v1/statements/$MONTH" -H "Authorization: Bearer $T_A")
CONSIST=$(echo "$STM" | python3 -c '
import sys,json
d=json.load(sys.stdin)
g=sum(c["grossMinor"] for c in d["byChannel"]); n=sum(c["count"] for c in d["byChannel"])
print("OK" if (g==d["grossMinor"] and n==d["rowCount"]) else f"MISMATCH gross {g} vs {d["grossMinor"]}, count {n} vs {d["rowCount"]}")
' 2>/dev/null)
[[ "$CONSIST" == "OK" ]]   && ok "C-1.14 statement totals reconcile with its own per-channel breakdown"   || bad "C-1.14 statement self-consistency" "$CONSIST"

head2 "Access control (PRD §13)"

# C-6.2 server-side merchant scoping
OTHER=$("${PSQL_OWNER[@]}" -c "SELECT id FROM collections WHERE merchant_id='$MCH_B' LIMIT 1" | tr -d '[:space:]')
SC=$(curl -s -o /dev/null -w '%{http_code}' "$API/v1/collections/$OTHER" -H "Authorization: Bearer $T_A")
[[ "$SC" == "404" ]] && ok "C-6.2 merchant A requesting merchant B's collection gets 404" \
  || bad "C-6.2 merchant scoping" "status=$SC for $OTHER"

# C-6.3 operations must not reach customer references or amounts through any endpoint
OPSC=$(curl -s -o /dev/null -w '%{http_code}' "$API/v1/collections/$OTHER" -H "Authorization: Bearer $T_OPS")
OPSRUN=$(curl -s -o /dev/null -w '%{http_code}' "$API/v1/ops/run-health" -H "Authorization: Bearer $T_OPS")
if [[ "$OPSC" == "403" && "$OPSRUN" == "200" ]]; then
  ok "C-6.3 operations refused collection detail (403) but sees run health (200)"
else
  bad "C-6.3 operations isolation" "collection=$OPSC runhealth=$OPSRUN"
fi

# Unauthenticated and malformed tokens
UNAUTH=$(curl -s -o /dev/null -w '%{http_code}' "$API/v1/collections?limit=1")
[[ "$UNAUTH" == "401" ]] && ok "C-6.6 an unauthenticated read is refused at the API layer (401)" \
  || bad "C-6.6 unauthenticated read" "status=$UNAUTH"

head2 "Residency (PRD §13)"

RES=$(curl -s "$API/v1/merchants/me" -H "Authorization: Bearer $T_A")
STMT=$(echo "$RES" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("dataRegionStatement",""))' 2>/dev/null)
[[ "$STMT" == *Kenya* ]] && ok "S-5.1 residency statement for a KE merchant: \"$STMT\"" \
  || bad "S-5.1 residency statement" "$RES"

CROSS=$("${PSQL_OWNER[@]}" -c "SELECT COUNT(*) FROM collections c JOIN merchants m ON m.id=c.merchant_id WHERE m.data_region <> c.origin_country" | tr -d '[:space:]')
[[ "$CROSS" == "0" ]] && ok "RES-1 no collection is stored against a merchant in the other region" \
  || bad "RES-1 residency" "$CROSS cross-region rows"

head2 "Observability"

LAG=$(curl -s "$API/metrics" | grep '^mopay_settlement_lag_seconds{' | awk '{print $2}')
[[ -n "$LAG" ]] && ok "OBS-4 settlement lag is exposed as a metric (${LAG%.*}s)" \
  || bad "OBS-4 lag metric" "not found in /metrics"

TP=$(curl -s -D - -o /dev/null "$API/healthz" | grep -i '^traceparent:' | tr -d '\r')
[[ -n "$TP" ]] && ok "OBS-5 responses carry W3C trace context ($TP)" \
  || bad "OBS-5 trace context" "no traceparent header"

printf '\n\033[1mResult:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
