# API contracts

Three OpenAPI 3.1 specifications. They are what the other components and the platform
read, so they are written before implementation and kept in step with it.

| Spec | Component | Visibility |
|---|---|---|
| `collections.openapi.yaml` | `collections-api` | Public. The console and merchant integrations call it |
| `ledger.openapi.yaml` | `ledger` | **Internal only.** Every path is under `/internal` |
| `merchants.openapi.yaml` | `merchant-api` | **Public, and deliberately discoverable.** A separate project; other teams are expected to find this contract and build against it |

## merchant-api is a separate project

`merchant-api` is the **system of record for merchant identity**. It holds its own
Postgres — separate instance, separate credentials, its own connection budget — and
nothing else on the platform may create a merchant.

mopay's local `merchants` table is a **projection** of it, not the source of truth. The
projection exists for two concrete reasons, both of which rule out removing it:

- five foreign keys point at `merchants` (`collections`, `fee_schedules`,
  `settlement_lines`, `payout_instructions`, `ledger_entries`);
- `ledger` reads merchant payout and fee data **inside the settlement commit
  transaction**, and an HTTP call does not belong inside a money transaction.

### 3. `collections-api` → `merchant-api`

| When | Operation | Why |
|---|---|---|
| Collection intake, merchant profile, balance screen | `GET /v1/merchants/{merchantId}` | Resolves the float limit and currency the C-1.5 check needs, and refreshes the local projection |

Cached for `MERCHANT_CACHE_TTL` (default 60s). Set it to `0` to call on every collection,
which makes the cross-project dependency visible in the platform's topology.

**This call is not fail-closed, and that is deliberate.** C-1.5 already makes the ledger a
hard dependency of intake. On a transport failure `collections-api` falls back to its
local projection and logs `serving merchant from the local projection`, so an outage in
another project degrades onboarding rather than stopping payments. Only a definitive
`404` means "this merchant cannot transact".

### Known gap

`merchant-api` never returns a full payout account number — reads get `payoutAccountMask`.
So the projection carries the mask, and a merchant onboarded through `merchant-api` will
produce payout instructions with a masked account. Resolving the real number for payout
execution needs a privileged endpoint that does not exist yet. Existing merchants are
unaffected: the projection only writes payout fields on INSERT, never on UPDATE.

### Deployment note (hard constraint 7)

One `merchant-api` serving both regions is fine under Compose. In production, constraint 7
(`prod-ke` and `prod-ng` share nothing) wants one instance per region, since a single
merchant store spanning both would be a shared datastore across the boundary.

## `ledger` owns the only database connection

`collections-api` and `settlement-worker` hold no Postgres pool. Every query they used to
run themselves is now an HTTP call to `ledger`'s `/internal/collections/...` and
`/internal/settlements/runs/...` surface — an internal RPC surface, uniform POST with a
JSON body, one endpoint per store method.

That is an architectural choice in service of a platform one: with a single component type
holding the pool, connection demand is exactly `replicas × DB_MAX_CONNS`, which a platform
rule can check before deployment rather than discovering under load.

## The internal call paths

`ledger` is never reachable from the console or from outside the project (README.md hard
constraint 8). Exactly two callers exist.

### 1. `collections-api` → `ledger`

| When | Operation | Why |
|---|---|---|
| Every intake request | `GET /internal/ledger/balances/{merchantId}` | The float-limit check (C-1.5). **Fails closed** — if this call fails, the collection is refused, never accepted |
| A callback clears a collection | `POST /internal/ledger/entries` | Writes the balancing pair for the collection (C-3.1). The money is not accounted for until the channel clears it |
| Collection detail is opened | `GET /internal/ledger/entries?eventType=collection&eventId=…` | C-1.9 / S-2 — the detail view shows the collection's ledger entries |
| The balance screen loads | `GET /internal/ledger/balances/{merchantId}` | C-1.11 — unsettled balance against the float limit |

### 2. The settlement run → `ledger`

| When | Operation | Why |
|---|---|---|
| A run starts | `GET /internal/settlements/pending` | The whole period in one unpaginated read. Full-period reconciliation (C-2.2) needs the complete set |
| Once per merchant in the run | `POST /internal/settlements/commit` | Settles one merchant atomically (C-2.6) and idempotently (C-2.7) |

> **Note on the second caller.** README constraint 9 asks that references to the
> settlement component stay inside its own directory and `deploy/`, because it may need to
> be removed from this repository as a `git rm -r` plus one deploy edit. The build plan
> (phase 2, not in this repository) explicitly asked this file to document *both* internal call
> paths. Those two instructions conflict. This file follows the build plan and names the
> path, since a contract that hides one of its two consumers is not much of a contract —
> but if constraint 9 is the one that matters, this section and the `ledger` spec's
> description are the two places to edit. **Flagged rather than silently resolved.**

## Conventions both specs follow

- **Money** is an integer in minor units, with an explicit currency. Never a float.
  `250.00 KES` is `25000` with `currency: KES`.
- **Time** is UTC, RFC3339, everywhere in storage and on the wire.
- **Errors** share one shape: a stable machine-readable `code`, a human `message`, and
  `details` carrying the relevant values. Every code is enumerated in both specs.
  `float_limit_exceeded` carries the current unsettled balance and the configured limit so
  a caller can act on it programmatically (C-1.5).
- **Auth** is a dev-mode bearer token carrying `sub`, `merchantId` and `role` — OIDC is a
  README.md non-goal. Authorization itself is enforced server-side and is not dev-mode.

## The unpaginated read

`GET /internal/settlements/pending` takes `region`, `periodStart` and `periodEnd`, and
nothing else. It has no `limit`, no `offset`, no `cursor` and no result cap.

This is deliberate (hard constraint 3). A period you can only see one page of cannot be
reconciled, and a settlement run must consider the whole period as one set to produce a
correct net amount per merchant. The caller holds the entire result.

It is a recorded design decision, not a defect. `seed volume --region NG --multiplier 40`
reports the row count and the serialised response size at month-end scale, so the cost of
this choice is measured rather than assumed.
