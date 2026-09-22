# mopay — Product Requirements Document

**Version 1.0 · 10 September 2026**

A merchant payment collections and settlement platform for Kenya and Nigeria, with a merchant-facing web console.

This document is self-contained. It is the complete build specification: an engineering team or coding agent should be able to implement the system from this document alone.

---

## 1. Overview

### 1.1 Problem

mopay is a payment collections intermediary. Merchants — mid-sized retailers, utilities and schools — accept payments from their customers over mobile money and card. mopay aggregates those collections, reconciles them against channel statements, and settles the net amount to each merchant's bank account.

The existing implementation is a set of spreadsheets plus a nightly script maintained by one engineer. It has three failure modes the business feels directly:

1. **Month-end reconciliation fails silently.** Merchants discover it when their settlement doesn't arrive.
2. **No merchant self-service.** Every "where is my money" question becomes a support ticket.
3. **Data residency is unmanaged.** Transaction data is stored wherever the script happened to run, which is a regulatory exposure in both markets.

### 1.2 Objective

Build a system that:

- merchants can serve themselves from, without contacting support for routine questions
- settles reliably at month-end, at forty times nightly volume, inside the same time window
- keeps each country's transaction data inside that country
- tells operations that a settlement run has failed before merchants notice

### 1.3 Success criteria

| | Measure |
|---|---|
| Merchant support load | 70% reduction in "where is my money" tickets within one quarter of launch |
| Settlement reliability | 100% of month-end runs complete inside the two-hour window over three consecutive months |
| Time to detect a failed run | Under 5 minutes, from over 12 hours today |
| Residency | Zero transaction-level records stored outside their origin country |

---

## 2. Users and roles

| Role | Who | Access |
|---|---|---|
| **Merchant user** | Operations staff at a merchant organisation | Their own merchant's data only. Collections, transaction detail, settlement runs, statements, settings |
| **Finance user** | mopay finance and reconciliation staff | Settlement totals and statements across all merchants. No customer-level detail |
| **Operations user** | mopay platform operations, on-call | Settlement run health across all merchants, failure reasons, retry control. **Must not see** per-transaction customer references or amounts |
| **Service client** | A merchant's own backend integration | Collections intake API only, scoped to that merchant |

End customers who pay merchants never interact with mopay. They are out of scope.

---

## 3. Scope

### 3.1 In scope

Collections intake across four channels · double-entry ledger · nightly and month-end settlement · payout instruction generation · merchant web console · operational alerting · role-based access · per-country data residency.

### 3.2 Out of scope

Merchant onboarding and KYC (merchants are provisioned administratively) · dispute and chargeback handling · foreign exchange and cross-currency settlement · payment initiation from the console · a mobile application · Ghana market launch (planned, not built).

---

## 4. Architecture

### 4.1 Components

Four independently deployable components.

| Component | Kind | Responsibility |
|---|---|---|
| **`collections-api`** | HTTP service | Collections intake, channel callbacks, read APIs for the console, retry control |
| **`ledger`** | HTTP service (internal only) | Double-entry ledger, balance computation |
| **`settlement-worker`** | Scheduled task | Nightly and month-end settlement runs. No inbound HTTP |
| **`mopay-console`** | Web application | Merchant and operations user interface |

`settlement-worker` is a scheduled task with no inbound HTTP surface. Settlement run state is persisted and read back through `collections-api`.

`ledger` is reachable only from `collections-api` and `settlement-worker`, never from the console or the public internet.

### 4.2 Environments

| Environment | Purpose | Data |
|---|---|---|
| `dev` | Integration | Synthetic |
| `staging` | Pre-production verification | Synthetic |
| `prod-ke` | Kenya production | Kenyan transaction data |
| `prod-ng` | Nigeria production | Nigerian transaction data |

Promotion paths: `dev → staging`, `staging → prod-ke`, `staging → prod-ng`.

`prod-ke` and `prod-ng` are fully independent deployments with independent datastores. They do not share transaction data. See §11.

### 4.3 Technical constraints

- Deploys to Kubernetes via OpenChoreo. Each component is a separate OpenChoreo component.
- All configuration through environment variables or mounted config. No environment-specific values in images.
- All secrets through the platform's secret mechanism. No credentials in source, images or config files.
- Stack choice is open. Pick one language for the services and one framework for the console, and be consistent.

---

## 5. Functional requirements

### 5.1 `collections-api` — collections intake

| # | Requirement |
|---|---|
| **C-1.1** | Accept a collection request from an authenticated merchant for a supported channel |
| **C-1.2** | Supported channels: `mpesa` (KE), `airtel_money` (KE), `paystack_card` (NG), `nibss_transfer` (NG) |
| **C-1.3** | **Idempotency.** A repeated request carrying the same `merchantReference` for the same merchant must return the original collection and must not create a second one |
| **C-1.4** | Persist every collection with: merchant, channel, amount, currency, customer reference, merchant reference, origin country, created timestamp, status |
| **C-1.5** | **Reject a collection when the merchant's unsettled balance exceeds their float limit.** Return HTTP 409 with error code `float_limit_exceeded`, the current unsettled balance and the configured limit, so the merchant's integration can act on it programmatically |
| **C-1.6** | Collection status lifecycle: `pending → cleared → settled`, or `pending → failed`. Status transitions are one-directional |
| **C-1.7** | Accept asynchronous channel callbacks that move a collection from `pending` to `cleared` or `failed`. Callbacks must be idempotent and must verify the channel's signature |
| **C-1.8** | Currency is derived from the channel, never accepted from the caller. `mpesa` and `airtel_money` are KES; `paystack_card` and `nibss_transfer` are NGN |

### 5.2 `collections-api` — read APIs

| # | Requirement |
|---|---|
| **C-1.9** | Retrieve a single collection by id, including its ledger entries |
| **C-1.10** | Search collections by customer reference, merchant reference, amount range, status or date range. Paginated |
| **C-1.11** | Return a merchant's current unsettled balance and float limit |
| **C-1.12** | Return collections aggregates for a date range: count, value, success rate, split by channel |
| **C-1.13** | List settlement runs with status, type, start and end time, row count and net amount. Filterable by merchant for merchant users; unfiltered for operations users |
| **C-1.14** | Return a monthly settlement statement for a merchant and month, with per-channel breakdown, as JSON and as CSV |
| **C-1.15** | Enqueue a retry of a failed settlement run. Operations users only |

### 5.3 `settlement-worker` — settlement

| # | Requirement |
|---|---|
| **C-2.1** | **Nightly run.** Aggregate the day's `cleared` collections per merchant, compute fees, produce a net settlement amount, mark the collections `settled` |
| **C-2.2** | **Month-end run.** On the last business day of the month, additionally reconcile the full month per merchant against channel statements and produce a monthly statement |
| **C-2.3** | A run produces a set of **immutable** settlement lines. Corrections are new lines with a reference to what they correct, never edits to existing lines |
| **C-2.4** | Emit one payout instruction per merchant per completed run, carrying the merchant's payout account and net amount |
| **C-2.5** | Record for every run: id, type, environment, status, started at, ended at, row count, and failure reason when failed |
| **C-2.6** | A run is atomic per merchant. A failure affecting one merchant must not leave another merchant's settlement half-written |
| **C-2.7** | A retried run must be idempotent. Re-running a period must not double-settle a collection |
| **C-2.8** | Fee computation: a per-merchant, per-channel percentage plus a fixed component, both configurable per merchant |

### 5.4 `settlement-worker` — alerting

| # | Requirement |
|---|---|
| **C-5.1** | Emit an alert when a settlement run fails |
| **C-5.2** | Emit an alert named `settlement-lag-critical` when settlement lag — the age of the oldest `cleared`, unsettled collection — exceeds 26 hours |
| **C-5.3** | Emit an alert when no run has completed in 26 hours |
| **C-5.4** | Every alert must carry the run id where applicable, the environment, and the number of affected merchants |

### 5.5 `ledger`

| # | Requirement |
|---|---|
| **C-3.1** | Double-entry record of every collection, fee, settlement and correction |
| **C-3.2** | Compute a merchant's balance at any point in time, including as of a past timestamp |
| **C-3.3** | Ledger entries are **append-only**. No update or delete API exists |
| **C-3.4** | Every entry references the business event that produced it — a collection id, settlement line id, or correction id |
| **C-3.5** | Entries always balance. Reject any write whose debits and credits do not sum to zero |

---

## 6. `mopay-console` — web application

### 6.1 Screens

| # | Screen | Roles | Content |
|---|---|---|---|
| **S-1** | Collections today | Merchant | Volume, value, success rate, split by channel. 24-hour sparkline |
| **S-2** | Transaction search | Merchant | Search by customer reference, merchant reference, amount or date. Detail view per collection showing its ledger entries |
| **S-3** | Settlement runs | Merchant | Run list: date, type, status, row count, net amount, payout status |
| **S-4** | Statements | Merchant, Finance | Monthly statement view and CSV download, per-channel breakdown |
| **S-5** | Settings | Merchant | Payout account, float limit (read-only), data region |
| **S-6** | Run health | Operations | All runs across merchants: status, duration against the window, failure reason, retry action |

### 6.2 Required states

These are requirements, not visual polish.

| # | Requirement |
|---|---|
| **S-3.1** | Every run displays exactly one of: `pending`, `running`, `completed`, `failed` |
| **S-3.2** | A `failed` run displays its failure reason inline, without requiring a click |
| **S-3.3** | A `running` run displays elapsed time against the two-hour window, and visually distinguishes a run that has exceeded it |
| **S-3.4** | **When the most recent completed run is older than 26 hours, every screen displays a persistent banner naming the last successful run time and the current lag.** The banner is dismissible per session but reappears on navigation. It must not be implemented as a transient toast |
| **S-5.1** | Settings states the country the merchant's transaction data is stored in, in plain language — for example "Your transaction data is stored in Nigeria" |
| **S-6.1** | Run health distinguishes a run that failed from a run that never started |
| **S-0.1** | Every screen has an explicit empty state, loading state and error state. An error state names what failed and offers a retry |

### 6.3 Console non-goals

No payment initiation, no merchant onboarding, no dispute management. The console reads and reconciles.

---

## 7. Data model

```
Merchant
  ├── id, name, country (KE | NG), createdAt
  ├── PayoutAccount        (1)  bank, accountNumber, accountName
  ├── FloatLimit           (1)  amount, currency
  ├── FeeSchedule          (many, per channel: percentage, fixed)
  └── dataRegion           (1)  KE | NG — immutable after creation

Collection
  ├── id, merchantId, channel, amount, currency
  ├── customerReference, merchantReference
  ├── originCountry
  ├── status  (pending | cleared | failed | settled)
  ├── createdAt, clearedAt, settledAt
  └── LedgerEntry (many)

SettlementRun
  ├── id, runType (nightly | monthEnd), environment
  ├── status (pending | running | completed | failed)
  ├── startedAt, endedAt, rowCount, failureReason
  ├── periodStart, periodEnd
  └── SettlementLine (many, immutable)
        ├── id, runId, merchantId
        ├── grossAmount, fees, netAmount, currency
        ├── correctsLineId (nullable)
        └── PayoutInstruction (0..1)

LedgerEntry  (append-only)
  ├── id, merchantId, direction (debit | credit)
  ├── amount, currency, createdAt
  ├── eventType, eventId
  └── transactionGroupId   — entries sharing this must sum to zero
```

**Immutability:** `LedgerEntry` and `SettlementLine` are append-only at the storage layer, not merely by convention. There is no code path that updates or deletes either.

`Merchant.dataRegion` and `Collection.originCountry` are the fields residency enforcement keys on.

---

## 8. Authentication and authorization

| # | Requirement |
|---|---|
| **C-6.1** | Console users authenticate via OIDC. Service clients authenticate via OAuth2 client credentials |
| **C-6.2** | A merchant user's token carries their merchant id. Every read is scoped to it server-side — **never filtered client-side** |
| **C-6.3** | An operations user sees run health across all merchants and **must not** be able to retrieve per-transaction customer references or amounts through any endpoint |
| **C-6.4** | A finance user sees settlement totals and statements across all merchants, and no customer-level detail |
| **C-6.5** | A service client may call collections intake and read its own merchant's collections. Nothing else |
| **C-6.6** | Authorization is enforced at the API layer. The console's navigation hiding a screen is not a control |
| **C-6.7** | Every authorization denial is logged with subject, action and resource |

---

## 9. Non-functional requirements

| # | Requirement |
|---|---|
| **NFR-1** | Collections intake: p99 under 400 ms, 99.9% monthly availability. It sits in the merchant's checkout path |
| **NFR-2** | Console: p95 page load under 2 seconds over a 3G connection. Assume mobile and constrained bandwidth are common |
| **NFR-3** | Nightly settlement completes inside a two-hour window, 01:00–03:00 local to the data region |
| **NFR-4** | **Month-end reconciliation processes approximately 40× the nightly row count and must complete inside the same two-hour window** |
| **NFR-5** | Nigerian transaction data is stored and processed within Nigeria; Kenyan within Kenya. See §11 |
| **NFR-6** | Ledger entries and settlement lines are immutable once written |
| **NFR-7** | Every settlement run is traceable end to end, from an alert back to the individual collections it touched |
| **NFR-8** | Launch volume ~180,000 collections per day across both markets, growing 15% quarter on quarter. Design for 12 months of that growth |
| **NFR-9** | A channel outage must degrade only that channel. Collections on other channels continue |
| **NFR-10** | No component requires a manual step to recover from a restart. All state is in the datastore |

---

## 10. Observability

| # | Requirement |
|---|---|
| **OBS-1** | Structured JSON logs on stdout. Every log line carries the trace id, merchant id where applicable, and environment |
| **OBS-2** | Request rate, error rate and duration exposed per endpoint for every HTTP component |
| **OBS-3** | `settlement-worker` exposes, per run: rows processed, duration, merchants settled, failures |
| **OBS-4** | Settlement lag exposed as a continuously updated metric, not computed only at run time — it is the signal C-5.2 alerts on |
| **OBS-5** | Distributed tracing across `collections-api` → `ledger` and `settlement-worker` → `ledger`. Trace context propagated on every internal call |
| **OBS-6** | Failure log messages name the specific condition and the affected identifiers. `"settlement failed"` is insufficient; `"settlement run <id> failed for merchant <id> at row <n>: <cause>"` is the standard |
| **OBS-7** | No customer references, account numbers or amounts in logs above debug level |

---

## 11. Data residency

| # | Requirement |
|---|---|
| **RES-1** | Transaction-level data for Nigerian merchants is stored and processed only within Nigeria. Transaction-level data for Kenyan merchants only within Kenya |
| **RES-2** | Transaction-level data means: collections, ledger entries, settlement lines, payout instructions, and anything containing a customer reference |
| **RES-3** | Aggregate, non-identifying totals may be centralised — per-country daily volume and value, run success counts |
| **RES-4** | `prod-ke` and `prod-ng` have fully independent datastores. No replication, no shared cache, no cross-region query path |
| **RES-5** | A merchant's `dataRegion` is set at creation and immutable |
| **RES-6** | No component in one production region may hold a network route or credential to the other region's datastore |
| **RES-7** | The console displays the storing country to the merchant (S-5.1). That statement must be true by construction, not by policy |

The drivers are Nigerian payment-data localisation requirements and Kenya's Data Protection Act 2019. Treat RES-1 as a hard constraint, not a preference.

---

## 12. Development seed data

Needed for the console to be reviewable and for settlement to be exercised.

- 12 merchants: 7 Kenyan, 5 Nigerian, with varied fee schedules and float limits
- One Kenyan and one Nigerian merchant deliberately near their float limit, to exercise C-1.5
- 90 days of collections across all four channels, ~2,000 per day, with a realistic 3–5% failure rate
- One month boundary inside the seeded range, so a month-end run is exercisable
- 90 days of settlement runs: mostly `completed`, at least one `failed` with a failure reason, at least one that exceeded the window

Seed data must be generated by a repeatable script, not committed as fixtures.

---

## 13. Acceptance criteria

The system is done when all of the following are demonstrable.

**Collections**
- [ ] A collection posted twice with the same `merchantReference` creates one record and returns the same id
- [ ] A collection for a merchant over their float limit returns 409 `float_limit_exceeded` with balance and limit
- [ ] A channel callback moves a collection to `cleared`; a replayed callback changes nothing
- [ ] An unsigned or wrongly signed callback is rejected

**Settlement**
- [ ] A nightly run settles the day's cleared collections and marks them `settled`
- [ ] A month-end run produces a monthly statement per merchant
- [ ] Re-running a settled period double-settles nothing
- [ ] A run failing mid-way leaves no merchant partially settled
- [ ] A settlement line cannot be updated or deleted through any API

**Ledger**
- [ ] Every collection, fee and settlement produces balancing entries
- [ ] A write whose entries do not sum to zero is rejected
- [ ] A merchant's balance as of a past date is computable and correct

**Console**
- [ ] Each of S-1 to S-6 renders with seed data
- [ ] Stopping `settlement-worker` for 27 hours produces the S-3.4 banner on every screen, naming the last successful run time
- [ ] A failed run shows its reason inline on S-3 and S-6
- [ ] Every screen has a distinct empty, loading and error state

**Access control**
- [ ] A merchant user cannot retrieve another merchant's collection by id
- [ ] An operations user cannot retrieve a customer reference through any endpoint
- [ ] Removing the console's client-side role check does not expose data the role may not see

**Residency**
- [ ] No `prod-ng` component holds a route or credential to the `prod-ke` datastore, or the reverse
- [ ] A Kenyan merchant's collections are absent from the Nigerian datastore
- [ ] S-5 states the correct country for merchants in both regions

**Operations**
- [ ] `settlement-lag-critical` fires within 5 minutes of lag exceeding 26 hours
- [ ] Every component recovers from a restart with no manual step
- [ ] A trace spans `collections-api` → `ledger` for a single collection

---

## 14. Glossary

| Term | Meaning |
|---|---|
| **Collection** | A single payment taken from a customer on behalf of a merchant |
| **Clearing** | A channel confirming a collection succeeded |
| **Settlement** | Aggregating cleared collections, deducting fees, and producing a net amount owed to a merchant |
| **Settlement line** | One merchant's settlement result within one run |
| **Payout** | The bank transfer that moves a settled amount to a merchant |
| **Float limit** | The maximum unsettled balance a merchant may accumulate before further collections are refused |
| **Settlement lag** | The age of the oldest cleared but unsettled collection |
| **Channel** | A payment method integration: M-Pesa, Airtel Money, Paystack card, NIBSS transfer |
