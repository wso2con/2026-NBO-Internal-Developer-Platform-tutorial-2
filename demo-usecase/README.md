# mopay

Merchant payment collections and settlement for Kenya and Nigeria. Merchants take payments
over mobile money and bank transfer; mopay aggregates those collections, keeps a
double-entry ledger, settles net amounts to merchants, and gives merchants a console.

This is a reference system for an OpenChoreo tutorial. It is genuinely working software —
real business logic, real persistence, real telemetry — built to `docs/mopay-prd.md` at the
scope agreed in `docs/SCOPE.md`.

## Documents

| File | What it is |
|---|---|
| `docs/mopay-prd.md` | The specification. All `§n` and `C-x.x` references resolve here |
| `docs/SCOPE.md` | **Binding.** Every requirement classified BUILD / THIN / SKIP, and the seven conflicts between the PRD and the hard constraints, resolved |
| `api/` | The two OpenAPI 3.1 contracts |

When the PRD and SCOPE disagree, SCOPE wins.

> **Note.** The specification is `docs/mopay-prd.md` — that is where every `§n` and
> `C-x.x` reference resolves. A ten-phase build plan once lived at `docs/PRD.md`; it is
> not in this repository and nothing here depends on it.

## Repository layout

```
demo-usecase/
├── collections-api/      public intake + read APIs      (Go)
├── ledger/               append-only ledger             (Go)  ← only DB connection
├── settlement-worker/    scheduled settlement run       (Go)
├── mopay-console/        merchant/ops web UI            (React + Vite, nginx)
├── merchant-api/         merchant identity              (Go)  ← separate project
├── seed/                 dataset generator              (Go)
├── loadgen/              load generator                 (Go)
├── db/migrations/        mopay schema, grants, house account
├── api/                  OpenAPI 3.1 contracts
├── docs/                 mopay-prd.md (spec) · SCOPE.md (binding)
└── scripts/              verification scripts
```

Each Go service follows the same shape: `cmd/<name>/main.go` is the entry point,
`internal/config/config.go` is the **single place** that reads environment variables,
`internal/api/` holds handlers, `internal/store/` holds persistence, `internal/platform/`
holds logging, errors and metrics.

## How the pieces fit

```
                    browser
                       │
        ┌──────────────┼───────────────┐
        ▼              ▼               ▼
  mopay-console  collections-api   merchant-api ─── merchant database
                       │                ▲            (separate datastore)
                       │                │
                       │    cross-project call, read-only
                       ▼
                    ledger ────────── mopay database
                       ▲
                       │
              settlement-worker
```

**`ledger` holds the only connection to the mopay database.** Everything else reaches data
through it. That is constraint 8a, and it is the reason the system's entire demand on
Postgres is one component's configuration.

`merchant-api` is the system of record for merchant identity and belongs to a different
team. mopay keeps a **projection** of its rows — filled on demand, never authoritative —
so a collection can carry a foreign key without mopay owning merchant data.

| Service | Listens | Published locally | Talks to |
|---|---|---|---|
| `collections-api` | 8080 | `8088` | `ledger`, `merchant-api` |
| `ledger` | 8080 | **nothing** — internal only | mopay database |
| `mopay-console` | 8080 | `5173` | `collections-api`, `merchant-api` *(from the browser)* |
| `settlement-worker` | — no inbound surface | — | `ledger` |
| `merchant-api` | 8080 | `8092` | merchant database |

### Configuration, per service

Every service validates its configuration at startup and **names every missing or invalid
variable at once** before exiting non-zero (constraint: fail fast, fail completely).

| Service | Environment variables |
|---|---|
| `ledger` | `DATABASE_URL` · `DB_MIN_CONNS` · `DB_MAX_CONNS` · `DATA_REGION` · `HOUSE_ACCOUNT_ID` · `PORT` · `LOG_LEVEL` · `QUERY_TIMEOUT` · `SHUTDOWN_GRACE` |
| `collections-api` | `LEDGER_BASE_URL` · `MERCHANT_API_BASE_URL` · `DATA_REGION` · `HOUSE_ACCOUNT_ID` · `PORT` · `LOG_LEVEL` · `CORS_ALLOWED_ORIGINS` · `LAG_REFRESH_INTERVAL` · `SETTLEMENT_STALENESS_THRESHOLD` · `SETTLEMENT_LAG_ALERT_THRESHOLD` · `LEDGER_TIMEOUT` · `MERCHANT_API_TIMEOUT` · `MERCHANT_CACHE_TTL` · `QUERY_TIMEOUT` · `SHUTDOWN_GRACE` |
| `settlement-worker` | `LEDGER_BASE_URL` · `DATA_REGION` · `RUN_MODE` · `RUN_WINDOW` · `RUN_INTERVAL` · `SETTLEMENT_PERIOD_HOURS` · `FORCE_MONTH_END` · `LEDGER_TIMEOUT` · `LOG_LEVEL` |
| `merchant-api` | `DATABASE_URL` · `DB_MIN_CONNS` · `DB_MAX_CONNS` · `ENVIRONMENT` · `PORT` · `LOG_LEVEL` · `CORS_ALLOWED_ORIGINS` · `QUERY_TIMEOUT` · `SHUTDOWN_GRACE` |
| `mopay-console` | `API_BASE_URL` · `MERCHANT_API_BASE_URL` — read at **container start**, written into `/config.js` and served to the browser, so one image works in every environment |

`DATA_REGION` is `KE` or `NG` and is rejected if it is anything else. `RUN_MODE` is `once`
or `interval`, required, no default — a scheduled run uses `once` and exits.

Settlement runs are performed by a separate scheduled component, `settlement-worker/`.
It is kept deliberately self-contained — see constraint 9.

## Data

**mopay database** — `merchants` (a projection) · `fee_schedules` · `collections` ·
`ledger_entries` · `settlement_runs` · `settlement_lines` · `payout_instructions`

**merchant database** — `merchants` (authoritative) · `fee_schedules`

Invariants enforced by the database, not by application code:

- `ledger_entries` and `settlement_lines` — the application role holds no `UPDATE` or
  `DELETE` grant. The migration verifies this and fails if the grant leaked back.
- `collections.status` moves one way only: `pending → cleared → settled`, or
  `pending → failed`. A trigger rejects anything else.
- `merchants.data_region` is immutable after insert — a trigger raises on change.
- `(merchant_id, merchant_reference)` is unique, which is how idempotent intake works.
- One settlement line per merchant per run, so a re-run cannot double-write.

Money is always integer minor units with an explicit currency. Fees are basis points. No
float ever touches a money value.

## API surface

**`collections-api`** — public. Dev-mode bearer tokens,
`Authorization: Bearer dev.<base64url({"sub":…,"merchantId":…,"role":…})>`. Authorization is
enforced **per route** and is real, not dev-mode: the wrong role gets a clean 403.

| Route | Roles |
|---|---|
| `POST /v1/collections` | merchant, service |
| `GET /v1/collections`, `/v1/collections/{id}` | merchant, service |
| `GET /v1/merchants/me`, `/v1/aggregates` | merchant |
| `GET /v1/statements/{month}` | merchant, finance |
| `GET /v1/ops/run-health` | operations |
| `GET /v1/settlement-runs`, `/v1/settlement-status` | any |
| `POST /v1/channel-callbacks` | channel callback |

Note the paths are `/v1/settlement-runs` and `/v1/settlement-status` — not
`/v1/settlements/…`.

**`ledger`** — every business route is under `/internal`, and the service publishes no
port. Balances, entry writes, pending settlement reads, and the atomic settlement commit.
`GET /internal/settlements/pending` is unpaginated by design (constraint 3).

**`merchant-api`** — `GET /v1/merchants`, `GET /v1/merchants/{id}`, `POST /v1/merchants`.
No delete: a merchant with collections behind it cannot meaningfully cease to exist.

All three expose `GET /healthz` and `GET /metrics`.

## Running it

Requires Docker. Everything runs locally under Compose.

```bash
make up      # build images, start Postgres, apply migrations, start the services
make seed    # generate the PRD §12 dataset (~9s)
make console # start the web console
make verify  # run the acceptance checks
```

| | |
|---|---|
| Console | http://localhost:5173 |
| collections-api | http://localhost:8088 |
| `ledger` | **no published port** — internal only, by design |

Ports are configurable in `.env` (`make env` generates it with random local passwords).
`8081` is a common collision, so collections-api defaults to `8088` here.

### Useful targets

```bash
make test      # the money-path tests
make reset     # truncate and reseed from scratch
make volume REGION=NG MULT=40   # month-end scale, and the response-size numbers
make logs      # tail every service
make clean     # stop everything and delete the database volume
```

## Hard constraints

These are not preferences. Each one is load-bearing, and violating it breaks something
downstream. The code cites them by number in comments.

1. **The ledger has no update or delete path for a ledger entry or a settlement line.**
   Not an API that refuses, not a soft delete — no code path exists. Enforced at the
   storage layer too: `UPDATE` and `DELETE` on those tables are revoked from the
   application database role (see `db/migrations/002_append_only_grants.sql`).

2. **Every ledger write must balance.** Entries sharing a `transactionGroupId` must sum to
   zero. A write that does not is rejected, with the group id and the actual sum in the
   error so the caller can act on it.

3. **`GET /internal/settlements/pending` is unpaginated by design.** No `limit`, no
   `offset`, no `cursor`, no result cap. It is the honest read path for full-period
   reconciliation: a period you can only see one page of cannot be reconciled. It is
   documented that way in `api/ledger.openapi.yaml`. **Do not add pagination to it, and do
   not file it as a defect.**

4. **Settlement lag is computed continuously by `collections-api`, not by the component
   that performs settlement.** It is the age of the oldest `cleared`, unsettled collection,
   published as a metric on a short interval, and it must keep being emitted when
   settlement is not running. A signal that dies with the thing it watches is not a signal.

5. **No environment-specific values in images.** Everything arrives as an environment
   variable. No hostnames, thresholds, regions or schedules baked in.

6. **No secrets in source, images, config files or commits.** `make env` generates `.env`
   locally with random passwords; it is gitignored and must stay that way.

7. **`prod-ke` and `prod-ng` share nothing.** Separate datastores, separate credentials, no
   replication, no shared cache, no cross-region query path. No component in one region
   may hold a route or a credential to the other region's datastore.

8. **The ledger is never reachable from the console or from outside the project.** Only
   `collections-api` and the settlement component call it. Under Compose this is enforced
   by publishing no port for it at all. The console reaches the data through
   `collections-api`, which reaches it through `ledger` — never directly.

   **8a. `ledger` is the only component that opens a Postgres connection.** Everything
   else goes through it. This is what lets the platform express one checkable rule —
   `replicas × DB_MAX_CONNS ≤ max_connections` — instead of trying to cap several
   independent pools and hope the sum fits. `ledger` defaults to a pool of 10/10 and reads
   `DB_MIN_CONNS` / `DB_MAX_CONNS` from the environment. It enforces no limit of its own —
   capping the value is a platform policy, not a component concern.

9. **The scheduled settlement component is kept self-contained.** References to it stay
   inside its own directory and the deployment configuration — it is not imported from, or
   referred to, by the other components' code or docs. It may need to be removed from this
   repository later, and that removal should be a directory delete plus one deployment edit.

## Conventions

- **Logging.** Structured JSON on stdout. Every line carries a trace id and the merchant
  id where applicable. **No mopay service reads an `ENVIRONMENT` variable**, and none
  stamps one on a log line, a metric label, an API response or a settlement run. The
  platform labels every line and series with the environment it collected them from; a
  value a service asserts about itself is a second source of truth that can disagree with
  that, and a deployment left on the wrong value mislabels the whole stream while the
  platform's label stays right. `DATA_REGION` is not the same kind of value and stays —
  it is a query predicate, not a label. (`merchant-api` belongs to another team and keeps
  its own `ENVIRONMENT`.) Failure messages name the specific condition and
  the affected identifiers: `"settlement failed"` is not acceptable;
  `"settlement run 8f21 failed for merchant mch_004 at row 1841: ledger write did not
  balance (group tg_99a2, sum -250.00 KES)"` is the standard.
- **Never logged above debug level:** customer references, account numbers, or amounts.
- **Errors over HTTP.** A JSON body with a stable machine-readable `code`, a human
  `message`, and the relevant values. `float_limit_exceeded` carries the current unsettled
  balance and the configured limit.
- **Money.** Integer minor units, never floats. Currency always explicit, and always
  derived from the channel — never accepted from a caller. Fee percentages are basis
  points, so no float touches a money value.
- **Time.** UTC everywhere in storage and APIs. Local time only for display and cron.
- **Config.** One place per service reads every environment variable, validates it at
  startup, and fails fast naming the missing or invalid variable.

## Things that look odd and are deliberate

Constraints 1, 3, 4 and 8 above are the ones most often mistaken for bugs. Two more:

- **The float-limit check fails closed.** If the ledger is unreachable, intake is refused
  rather than accepted. Accepting money we cannot account for is the worse failure.
- **A merchant asking for another merchant's collection gets 404, not 403.** The existence
  of the id is not theirs to learn.

`make verify` proves all of these against the running stack.

## Verification

`make verify` runs 17 checks against the running stack, and
`scripts/verify-settlement.sh` runs 9 more that drive settlement deliberately — including
per-merchant atomicity under an induced mid-run failure, and confirmation that the lag
metric keeps updating with settlement stopped. Each check names the requirement it proves.
