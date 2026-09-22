#!/usr/bin/env bash
# Scenario 2 — the deployment-time configuration failure.
#
# Nothing in this repository is modified to produce it. The failure lives entirely in the
# deployment artifact: a value that is reasonable, wrong, and impossible to hit locally,
# because locally .env supplies every variable and on the platform the Workload does.
#
#   ./scripts/config-failure.sh            # reproduce it against the built image
#   ./scripts/config-failure.sh fixed      # same, with the corrected value
#
# Requires the settlement-worker image (`make up` once is enough).
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

REGION="prod-ng"   # the OpenChoreo environment's name - a reasonable guess, and wrong
[ "${1:-broken}" = fixed ] && REGION="NG"

echo "DATA_REGION=$REGION"
echo "---"
set +e
docker compose run --rm --no-deps \
  -e DATA_REGION="$REGION" \
  -e RUN_MODE=once \
  settlement-worker
echo "--- exit=$?"
set -e

cat <<'NOTE'

The same two states as Workload environment variables:

  # what the agent writes first
  - name: DATA_REGION
    value: "prod-ng"        # the environment's name

  # what the component accepts
  - name: DATA_REGION
    value: "NG"

The allowed values are in settlement-worker/internal/config/config.go
(OneOf("DATA_REGION", "KE", "NG")) and in .env.example. Neither is something the
platform hands an agent authoring a Workload.
NOTE
