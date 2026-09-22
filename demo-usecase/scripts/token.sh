#!/usr/bin/env bash
# Prints a dev-mode bearer token.
#
# SCOPE conflict 5: these replace OIDC for the demo. They are NOT security - the payload
# is unsigned base64 JSON and anyone can forge one. They carry identity so that
# AUTHORIZATION, which is enforced server-side and is not dev-mode, has something to act
# on.
#
#   ./scripts/token.sh operations                 # cross-merchant role, no merchant id
#   ./scripts/token.sh merchant mch_001           # scoped to one merchant
#   ./scripts/token.sh service  mch_001           # what collections-api mints internally
#
#   curl -H "Authorization: Bearer $(./scripts/token.sh operations)" localhost:8092/v1/merchants
set -euo pipefail

role="${1:-}"
merchant="${2:-}"

case "$role" in
  merchant|service)
    if [ -z "$merchant" ]; then
      echo "role '$role' is merchant-scoped and needs a merchant id: $0 $role mch_001" >&2
      exit 2
    fi
    ;;
  finance|operations) ;;
  *)
    echo "usage: $0 <merchant|service|finance|operations> [merchantId]" >&2
    exit 2
    ;;
esac

python3 -c "import base64,json,sys;print('dev.'+base64.urlsafe_b64encode(json.dumps({'sub':'cli@mopay','merchantId':sys.argv[2],'role':sys.argv[1]}).encode()).decode().rstrip('='))" \
  "$role" "$merchant"
