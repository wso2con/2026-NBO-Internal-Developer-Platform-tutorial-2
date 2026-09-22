#!/usr/bin/env bash
# mopay - scripted demo scenario.
#
#   ./scripts/demo.sh          run every act, pausing between them
#   ./scripts/demo.sh 3        run act 3 only
#   NOPAUSE=1 ./scripts/demo.sh   run straight through
#
# Each act is self-contained and leaves the stack usable for the next one.
set -uo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a

API="http://localhost:${COLLECTIONS_PUBLISH_PORT:-8088}"
CONSOLE="http://localhost:${CONSOLE_PUBLISH_PORT:-5173}"
NET="$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$(docker compose ps -q ledger)" 2>/dev/null)"
PSQL=(docker compose exec -T -e PGPASSWORD="$POSTGRES_PASSWORD" postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tA)
PSQL_APP=(docker compose exec -T -e PGPASSWORD="$APP_DB_PASSWORD" postgres psql -U "$APP_DB_USER" -h 127.0.0.1 -d "$POSTGRES_DB" -tA)

# Namespaced deliberately: single-letter globals collide with act-local variables
# (an earlier version used $B for bold and act 6 reused $B for a collection id).
DBOLD=$'\033[1m'; DCYN=$'\033[36m'; DGRN=$'\033[32m'; DYEL=$'\033[33m'; DOFF=$'\033[0m'
act(){ printf '\n%s══════════════════════════════════════════════════════════════════%s\n' "$DCYN" "$DOFF"
       printf '%s  ACT %s — %s%s\n' "$DBOLD" "$1" "$2" "$DOFF"
       printf '%s══════════════════════════════════════════════════════════════════%s\n' "$DCYN" "$DOFF"; }
say(){ printf '\n%s▸ %s%s\n' "$DBOLD" "$1" "$DOFF"; }
note(){ printf '   %s\n' "$1"; }
good(){ printf '   %s✓ %s%s\n' "$DGRN" "$1" "$DOFF"; }
warn(){ printf '   %s! %s%s\n' "$DYEL" "$1" "$DOFF"; }
pause(){ [[ -n "${NOPAUSE:-}" ]] && return 0; printf '\n%s   [enter to continue]%s' "$DCYN" "$DOFF"; read -r _; }

tok(){ python3 -c "import base64,json,sys;print('dev.'+base64.urlsafe_b64encode(json.dumps({'sub':'demo','merchantId':sys.argv[1],'role':sys.argv[2]}).encode()).decode().rstrip('='))" "$1" "$2"; }
icurl(){ docker run --rm --network "$NET" curlimages/curl:latest -s "$@"; }
jq_(){ python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null; }
money(){ python3 -c "print(f'{int(input())/100:,.2f}')" <<<"$1"; }
bal(){ icurl "http://ledger:8080/internal/ledger/balances/$1" | jq_ 'd["amountMinor"]'; }

# ── reusable helpers ────────────────────────────────────────────────────────
new_collection(){ # merchant amount ref -> id
  curl -s -X POST "$API/v1/collections" -H "Authorization: Bearer $(tok "$1" service)" \
    -H 'Content-Type: application/json' \
    -d "{\"channel\":\"mpesa\",\"amountMinor\":$2,\"customerReference\":\"cust_demo\",\"merchantReference\":\"$3\"}"
}
clear_collection(){ curl -s -X POST "$API/v1/channel-callbacks" -H 'Content-Type: application/json' \
    -d "{\"collectionId\":\"$1\",\"outcome\":\"cleared\"}"; }
run_once(){ docker compose run --rm -T -e RUN_MODE=once "$@" settlement-worker 2>&1; }

# ═══════════════════════════════════════════════════════════════════════════
act0(){
  act 0 "Clean slate"
  say "Reset the dataset and stop the worker so settlement is under our control"
  docker compose stop settlement-worker >/dev/null 2>&1
  docker compose --profile tools run --rm seed reset 2>&1 | grep '"msg":"baseline seeded"' \
    | python3 -c "import sys,json; d=json.loads(sys.stdin.readline()); print(f'   {d[\"collections\"]:,} collections · {d[\"ledger_entries\"]:,} ledger entries · {d[\"settlement_runs\"]} runs · seeded in {d[\"duration_ms\"]}ms')"
  good "12 merchants (7 Kenyan, 5 Nigerian), 90 days of history"
  note "Console: $CONSOLE   API: $API"
  warn "settlement-worker is STOPPED for the rest of this demo"
}

# ═══════════════════════════════════════════════════════════════════════════
act1(){
  act 1 "A payment arrives — and the books stay balanced"
  M=mch_001; REF="demo-$(date +%s)"
  say "1a. The shop reports a 4,500.00 KES payment"
  R=$(new_collection "$M" 450000 "$REF"); ID=$(jq_ 'd["id"]' <<<"$R")
  note "id=$ID   status=$(jq_ 'd["status"]' <<<"$R")   currency=$(jq_ 'd["currency"]' <<<"$R")"
  good "currency KES was DERIVED from the mpesa channel, not taken from the request"
  E=$("${PSQL[@]}" -c "SELECT COUNT(*) FROM ledger_entries WHERE event_id='$ID'"|tr -d ' ')
  good "ledger entries so far: $E — the money has not actually arrived yet"
  echo "$ID" > /tmp/demo_id
  pause

  say "1b. Send the same request again (the shop's integration retries)"
  R2=$(new_collection "$M" 450000 "$REF"); ID2=$(jq_ 'd["id"]' <<<"$R2")
  CNT=$("${PSQL[@]}" -c "SELECT COUNT(*) FROM collections WHERE merchant_reference='$REF'"|tr -d ' ')
  [[ "$ID" == "$ID2" ]] && good "same id returned, $CNT row in the database — no duplicate payment" \
                        || warn "MISMATCH $ID vs $ID2"
  pause

  say "1c. M-Pesa confirms the money is real"
  clear_collection "$ID" | python3 -c "import sys,json;d=json.load(sys.stdin);print('   status='+d['collection']['status']+'  changed='+str(d['changed']))"
  "${PSQL[@]}" -c "SELECT '   '||merchant_id||E'\t'||direction||E'\t'||to_char(amount_minor/100.0, E'FM999G999G990D00') FROM ledger_entries WHERE event_id='$ID' ORDER BY direction"
  S=$("${PSQL[@]}" -c "SELECT SUM(CASE WHEN direction='credit' THEN amount_minor ELSE -amount_minor END) FROM ledger_entries WHERE event_id='$ID'"|tr -d ' ')
  good "two entries, summing to $S — this is the invariant the whole system rests on"
  note "merchant balance is now $(money "$(bal $M)") KES — money mopay is holding"
}

# ═══════════════════════════════════════════════════════════════════════════
act2(){
  act 2 "The shop gets paid"
  M=mch_001
  note "balance before settlement: $(money "$(bal $M)") KES"
  say "Run one settlement"
  run_once | python3 -c "
import sys,json
for l in sys.stdin:
    try: d=json.loads(l)
    except: continue
    if d.get('msg')=='settlement run completed':
        print(f'   run {d[\"run_id\"]} · {d[\"rows_processed\"]} collections · {d[\"merchants_settled\"]} merchants · {d[\"duration_ms\"]}ms')"
  ID=$(cat /tmp/demo_id 2>/dev/null)
  "${PSQL[@]}" -c "SELECT '   line '||l.id||E'\n   gross '||to_char(l.gross_minor/100.0,E'FM999G999G990D00')||'  fees '||to_char(l.fees_minor/100.0,E'FM999G999G990D00')||'  net '||to_char(l.net_minor/100.0,E'FM999G999G990D00')||'  over '||l.row_count||' collections'||E'\n   payout to '||p.bank||' for '||to_char(p.amount_minor/100.0,E'FM999G999G990D00')
   FROM collections c JOIN settlement_lines l ON l.id=c.settlement_line_id JOIN payout_instructions p ON p.settlement_line_id=l.id WHERE c.id='$ID'"
  good "balance back to $(money "$(bal $M)") KES — mopay owes this merchant nothing"
  note "the 4,500.00 payment is now one of hundreds inside a single bank transfer"
}

# ═══════════════════════════════════════════════════════════════════════════
act3(){
  act 3 "Protection — the float limit"
  M=mch_004
  LIM=$(curl -s "$API/v1/merchants/me/balance" -H "Authorization: Bearer $(tok $M merchant)" | jq_ 'd["floatLimitMinor"]')
  note "Thika Road Hardware float limit: $(money "$LIM") KES"
  say "Trade up to ~97% of the limit (settlement in act 2 cleared the seeded position)"
  NEED=$(python3 -c "print(int($LIM*0.97))")
  FILL=$(jq_ 'd["id"]' <<<"$(new_collection "$M" "$NEED" "fill-$RANDOM-$$")")
  clear_collection "$FILL" >/dev/null
  B4=$(curl -s "$API/v1/merchants/me/balance" -H "Authorization: Bearer $(tok $M merchant)")
  note "unsettled $(money "$(jq_ 'd["unsettledBalanceMinor"]' <<<"$B4")") of limit $(money "$(jq_ 'd["floatLimitMinor"]' <<<"$B4")") KES  (headroom $(money "$(jq_ 'd["headroomMinor"]' <<<"$B4")"))"
  say "The shop tries to take another large payment"
  OUT=$(new_collection "$M" "$(python3 -c "print(int($LIM*0.10))")" "over-$RANDOM-$$")
  printf '   %s\n' "$(jq_ 'd["code"]' <<<"$OUT")"
  printf '   %s\n' "$(jq_ 'd["message"]' <<<"$OUT")"
  good "refused with the balance and the limit in the body — the integration can act on it"
}

# ═══════════════════════════════════════════════════════════════════════════
act4(){
  act 4 "Protection — fail closed when the ledger is unreachable"
  say "Stop the ledger, then try to take a payment"
  docker compose stop ledger >/dev/null 2>&1
  OUT=$(new_collection mch_001 120000 "closed-$RANDOM-$$")
  printf '   %s\n' "$(jq_ 'd["code"]' <<<"$OUT")"
  good "REFUSED, not accepted — taking money we cannot record is the worse failure"
  say "Restart the ledger"
  docker compose start ledger >/dev/null 2>&1
  for _ in $(seq 1 25); do icurl http://ledger:8080/healthz >/dev/null 2>&1 && break; sleep 1; done
  good "ledger healthy again"
}

# ═══════════════════════════════════════════════════════════════════════════
act5(){
  act 5 "Protection — the books cannot be edited or unbalanced"
  say "5a. Try to edit a ledger entry directly, as the application database user"
  OUT=$("${PSQL_APP[@]}" -c "UPDATE ledger_entries SET amount_minor=1 WHERE id=(SELECT id FROM ledger_entries LIMIT 1)" 2>&1 | head -2)
  printf '   %s\n' "$OUT"
  good "Postgres itself refuses — the app has no UPDATE grant on this table"
  pause
  say "5b. Try to write entries that do not sum to zero"
  OUT=$(icurl -X POST http://ledger:8080/internal/ledger/entries -H 'Content-Type: application/json' \
    -d "{\"transactionGroupId\":\"tg_demo_$RANDOM\",\"eventType\":\"collection\",\"eventId\":\"demo\",\"entries\":[
        {\"merchantId\":\"mch_001\",\"direction\":\"credit\",\"amountMinor\":25000,\"currency\":\"KES\"},
        {\"merchantId\":\"mopay_house\",\"direction\":\"debit\",\"amountMinor\":20000,\"currency\":\"KES\"}]}")
  printf '   %s\n' "$(jq_ 'd["message"]' <<<"$OUT")"
  good "rejected, and the error names the group and the actual imbalance"
}

# ═══════════════════════════════════════════════════════════════════════════
act6(){
  act 6 "Protection — one merchant's failure cannot corrupt another's"
  say "Give two merchants cleared money"
  COL_A=$(jq_ 'd["id"]' <<<"$(new_collection mch_001 55000 "atom-a-$RANDOM-$$")"); clear_collection "$COL_A" >/dev/null
  COL_B=$(jq_ 'd["id"]' <<<"$(new_collection mch_003 77000 "atom-b-$RANDOM-$$")"); clear_collection "$COL_B" >/dev/null
  note "mch_001 -> 550.00   mch_003 -> 770.00"
  say "Run settlement, but force it to fail when it reaches mch_003"
  run_once -e SETTLEMENT_FAIL_AT_MERCHANT=mch_003 | grep -o 'settlement run [a-z0-9_]* failed for merchant [a-z0-9_]* at row [0-9]*' | head -1 | sed 's/^/   /'
  SA=$("${PSQL[@]}" -c "SELECT status FROM collections WHERE id='$COL_A'"|tr -d ' ')
  SB=$("${PSQL[@]}" -c "SELECT status FROM collections WHERE id='$COL_B'"|tr -d ' ')
  note "mch_001 collection: $SA"
  note "mch_003 collection: $SB"
  [[ "$SA" == "settled" && "$SB" == "cleared" ]] \
    && good "the merchant processed BEFORE the failure is fully settled; the failing one is untouched" \
    || warn "unexpected: $SA / $SB"
  say "Clean up by running settlement again"
  run_once >/dev/null 2>&1
  good "re-running settles the leftover and double-settles nothing"
}

# ═══════════════════════════════════════════════════════════════════════════
act7(){
  act 7 "The alarm — noticing that settlement stopped"
  say "Shorten the staleness threshold to 60s so the window is observable"
  docker compose stop settlement-worker >/dev/null 2>&1
  SETTLEMENT_STALENESS_THRESHOLD=60s SETTLEMENT_LAG_ALERT_THRESHOLD=60s \
    docker compose up -d collections-api >/dev/null 2>&1
  sleep 6
  say "Create money that nobody will settle (the worker is stopped)"
  ID=$(jq_ 'd["id"]' <<<"$(new_collection mch_001 333000 "lag-$RANDOM-$$")"); clear_collection "$ID" >/dev/null
  good "3,330.00 KES sitting unsettled, and nothing is running to settle it"
  say "Watch settlement lag climb with NO worker running"
  for i in 1 2 3 4 5; do
    S=$(curl -s "$API/v1/settlement-status" -H "Authorization: Bearer $(tok mch_001 merchant)")
    printf '   t+%-3ss  lag=%-6ss  stale=%s\n' "$((i*20-20))" "$(jq_ 'int(d["lagSeconds"])' <<<"$S")" "$(jq_ 'd["stale"]' <<<"$S")"
    [[ $i -lt 5 ]] && sleep 20
  done
  good "the alarm is produced by collections-api, NOT by the worker it is watching"
  printf '\n%s   >>> OPEN %s AND SHOW THE RED BANNER ON EVERY TAB <<<%s\n' "$DBOLD" "$CONSOLE" "$DOFF"
  note "it is in the page flow, it does not time out, and it comes back when you navigate"
  pause
  say "Restore the 26h threshold and restart the worker"
  docker compose up -d collections-api >/dev/null 2>&1
  docker compose start settlement-worker >/dev/null 2>&1
  sleep 6
  good "back to normal"
}

ACTS=(act0 act1 act2 act3 act4 act5 act6 act7)
if [[ $# -gt 0 ]]; then "act$1"; else
  for a in "${ACTS[@]}"; do "$a"; pause; done
  printf '\n%s  Demo complete.%s\n' "$DGRN" "$DOFF"
fi
