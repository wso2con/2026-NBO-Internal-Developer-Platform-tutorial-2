# SCOPE.md — what we are actually building

**Status:** draft for review · 12 September 2026
**Authority:** the project rule is *"when the PRD and SCOPE disagree, SCOPE wins."* This file is
therefore the binding statement of work. It classifies every numbered requirement in
`docs/mopay-prd.md` as **BUILD**, **THIN**, or **SKIP**.

- **BUILD** — implemented properly, tested where it touches money.
- **THIN** — endpoint/screen exists with the correct shape and simplified behaviour.
- **SKIP** — not built. Listed here so its absence is a decision, not an oversight.

## A note on file naming

The specification is **`docs/mopay-prd.md`** — that is where every `§n` and `C-x.x`
reference resolves.

A separate ten-phase build plan was also briefly present at `docs/PRD.md`. It is **not in
this repository**, and nothing here depends on it: it described how the system was to be
built, not how it behaves. Where this file cites "the build plan", that is what it means.

| File | What it is |
|---|---|
| `docs/mopay-prd.md` | The specification. All `§n` and `C-x.x` references resolve here |
| `docs/SCOPE.md` | This file. Binding — it overrides the PRD where they disagree |

All section references below point at `docs/mopay-prd.md`.

---

## 1. Conflicts between the PRD and the hard constraints

`README.md`'s non-goals list contradicts the PRD in seven places. Per the authority rule, the
non-goal wins each time. Flagging all seven explicitly, because three of them delete an
acceptance criterion from §13 and you should agree to that rather than discover it.

| # | PRD says | The constraints say | Resolution | §13 impact |
|---|---|---|---|---|
| 1 | **C-1.2** four channels: `mpesa`, `airtel_money`, `paystack_card`, `nibss_transfer` | Non-goal: "more than two payment channels — one Kenyan (`mpesa`), one Nigerian (`nibss_transfer`)" | **Two channels.** `mpesa` (KES), `nibss_transfer` (NGN) | §12 seed "across all four channels" becomes across both |
| 2 | **C-1.7** callbacks "must verify the channel's signature" | Non-goal: "channel callback signature verification" | **No signature verification.** Callback idempotency still BUILD | ❌ Deletes *"An unsigned or wrongly signed callback is rejected"* |
| 3 | **C-1.14** statement "as JSON and as CSV" | Non-goal: "CSV export or file downloads" | **JSON only.** S-4 renders on screen | Weakens S-4 |
| 4 | **C-1.15** enqueue retry of a failed run; **S-6** retry action | Non-goal: "settlement run retry endpoints or retry buttons" | **SKIP both.** S-6 shows health, no retry control | Removes retry from S-6 |
| 5 | **C-6.1** OIDC + OAuth2 client credentials | Non-goal: "OIDC, an identity provider, login screens, or session management" | **Dev-mode bearer tokens** carrying `sub`, `merchantId`, `role` | C-6.2–C-6.7 unaffected — still enforced server-side |
| 6 | **C-2.3** corrections are new lines referencing what they correct | Non-goal: "correction settlement lines" | **THIN.** `correctsLineId` column exists, nullable, always NULL. No correction code path | Immutability criterion still holds |
| 7 | **C-2.2 / §5.4** alerting sits under `settlement-worker` | Hard constraint 4: lag "computed continuously by `collections-api`, not by `settlement-worker`" | **`collections-api` owns the lag metric.** It must keep emitting with the worker stopped | Strengthens the C-5.2 criterion |

**Two further scope calls I am making, flagged for you rather than taken silently:**

- **NFR-8 / NFR-4 volume.** The PRD designs for ~180,000 collections/day growing 15% QoQ, and
  month-end at 40× nightly. Seeding 90 days at that rate is ~16M rows — minutes to seed and
  slow to demo. **Seed at the §12 figure (~2,000/day ≈ 180k rows total)**, and leave
  `seed volume --multiplier 40` as the lever that reproduces month-end scale on demand. The
  capacity numbers the build plan asks us to record (per-record bytes, full response size)
  are measured and written down, so the 40× figure stays *calculable* without being *resident*.
- **Hard constraint 3 (unpaginated `/internal/settlements/pending`) is deliberate.** It is the
  honest full-period reconciliation read. It is **not** a defect, it does not get pagination,
  and it does not get flagged in review. Recorded here so nobody "fixes" it later.

---

## 2. `collections-api` — intake (§5.1)

| # | Class | Note |
|---|---|---|
| C-1.1 | **BUILD** | Authenticated intake |
| C-1.2 | **BUILD** (reduced) | Two channels only — conflict 1 |
| C-1.3 | **BUILD** | Idempotency via unique constraint on `(merchant_id, merchant_reference)`, not an application check |
| C-1.4 | **BUILD** | Full persisted shape per §7 |
| C-1.5 | **BUILD** | 409 `float_limit_exceeded` carrying balance + limit. **Fails closed** if the ledger is unreachable |
| C-1.6 | **BUILD** | One-directional lifecycle, enforced in SQL |
| C-1.7 | **BUILD** (reduced) | Idempotent callbacks, **no** signature check — conflict 2 |
| C-1.8 | **BUILD** | Currency derived from channel, never from the caller |

## 3. `collections-api` — read APIs (§5.2)

| # | Class | Note |
|---|---|---|
| C-1.9 | **BUILD** | Collection by id with its ledger entries |
| C-1.10 | **THIN** | Search on merchant ref, status, date range. Amount-range and customer-ref search omitted; paginated |
| C-1.11 | **BUILD** | Unsettled balance + float limit. Feeds S-5 and the C-1.5 check |
| C-1.12 | **BUILD** | Aggregates for a date range, split by channel. Drives S-1 |
| C-1.13 | **BUILD** | Run list, merchant-filtered for merchants, unfiltered for operations |
| C-1.14 | **THIN** | Monthly statement as **JSON only** — conflict 3 |
| C-1.15 | **SKIP** | Retry — conflict 4 |
| — | **BUILD** | **Settlement lag metric**, continuously recomputed on a configurable interval — constraint 4. Plus most-recent-completed-run timestamp for the S-3.4 banner |

## 4. `settlement-worker` (§5.3)

| # | Class | Note |
|---|---|---|
| C-2.1 | **BUILD** | Nightly: aggregate cleared, compute fees, net amount, mark settled |
| C-2.2 | **BUILD** (reduced) | Month-end statement per merchant. "Reconcile against channel statements" is **THIN** — no channel statement source exists in this system, so reconciliation is against our own cleared set |
| C-2.3 | **THIN** | Lines immutable — enforced. Corrections — conflict 6 |
| C-2.4 | **BUILD** | One payout instruction per merchant per completed run |
| C-2.5 | **BUILD** | Full run record incl. failure reason |
| C-2.6 | **BUILD** | **One transaction per merchant.** Tested under induced mid-run failure |
| C-2.7 | **BUILD** | Idempotent, keyed on the collection not the run |
| C-2.8 | **BUILD** | Per-merchant per-channel percentage + fixed component |

## 5. `ledger` (§5.5)

Everything here is BUILD. This is the component the whole demo rests on.

| # | Class | Note |
|---|---|---|
| C-3.1 | **BUILD** | Double-entry for collection, fee, settlement |
| C-3.2 | **BUILD** | Balance incl. **as of a past timestamp** |
| C-3.3 | **BUILD** | Append-only. No update/delete code path, **and** `REVOKE UPDATE, DELETE` from the app role in a migration — hard constraint 1 |
| C-3.4 | **BUILD** | Every entry references its business event |
| C-3.5 | **BUILD** | Sum-zero or reject, error naming group id + actual sum — hard constraint 2 |

## 6. Alerting (§5.4)

| # | Class | Note |
|---|---|---|
| C-5.1 | **THIN** | Run failure surfaces as a structured log + metric. No paging integration under Compose |
| C-5.2 | **BUILD** | `settlement-lag-critical`, threshold from config (default 26h), fed by the `collections-api` metric |
| C-5.3 | **THIN** | Derived from the same last-completed-run signal as S-3.4 |
| C-5.4 | **BUILD** | Alert payload carries run id, environment, affected merchant count |

## 7. Console (§6)

| # | Class | Note |
|---|---|---|
| S-1 | **BUILD** | Collections today, per-channel split, 24h sparkline |
| S-2 | **BUILD** | List + detail showing ledger entries |
| S-3 | **BUILD** | Settlement runs |
| S-4 | **THIN** | Single month's statement on screen, no CSV — conflict 3 |
| S-5 | **BUILD** | Payout account, float limit read-only, data region |
| S-6 | **BUILD** (reduced) | Run health across merchants, **no retry action** — conflict 4 |
| S-3.1 | **BUILD** | Exactly one of four states |
| S-3.2 | **BUILD** | Failure reason inline, no click |
| S-3.3 | **BUILD** | Elapsed vs two-hour window, exceeded state distinguished |
| **S-3.4** | **BUILD** | **Persistent banner on every screen.** Dismissible per session, reappears on navigation, **not a toast**. Threshold configurable, default 26h. *The build plan names this the most demo-visible element in the system — it does not get dropped* |
| S-5.1 | **BUILD** | Plain-language residency statement |
| S-6.1 | **BUILD** | Failed vs never-started distinguished |
| S-0.1 | **BUILD** | Empty / loading / error on every screen; error names what failed and offers retry |

## 8. Auth (§8)

C-6.1 **THIN** (dev tokens — conflict 5). C-6.2 – C-6.7 all **BUILD**: server-side merchant
scoping, operations role blocked from customer references and amounts, enforcement at the API
layer, denials logged with subject/action/resource. The §13 criterion *"removing the console's
client-side role check does not expose data the role may not see"* stays live and is testable.

## 9. NFRs (§9)

| # | Class | Note |
|---|---|---|
| NFR-1 | **THIN** | Sane latency, not load-tested |
| NFR-2 | **BUILD** | Console kept light; no heavy chart library |
| NFR-3 | **BUILD** | Window modelled; cron configurable per environment |
| NFR-4 | **THIN** | 40× reproducible via `seed volume`, not resident — see §1 |
| NFR-5 | **BUILD** | Residency — see §11 below |
| NFR-6 | **BUILD** | Enforced at the storage layer |
| NFR-7 | **BUILD** | Run → settlement line → collection → ledger entry all traceable |
| NFR-8 | **SKIP** | 12-month growth capacity work is post-build |
| NFR-9 | **THIN** | Channel isolation follows from per-channel independence; no breaker |
| NFR-10 | **BUILD** | All state in Postgres, no manual recovery step |

## 10. Observability (§10)

OBS-1 **BUILD** · OBS-2 **BUILD** · OBS-3 **BUILD** · OBS-4 **BUILD** (constraint 4) ·
OBS-5 **BUILD** (W3C `traceparent` propagated on every internal call) · OBS-6 **BUILD**
(failure messages name condition + identifiers; audited in the final phase) ·
OBS-7 **BUILD** (no customer references, account numbers or amounts above debug).

## 11. Residency (§11)

RES-1 – RES-7 all **BUILD** *as far as Compose can demonstrate*. Under Docker Compose this is
separate databases with separate credentials and no cross-route — real enforcement (network
policy, independent datastores per environment) lands with the OpenChoreo phases. RES-5
(`dataRegion` immutable) and RES-7 (S-5 true by construction, read from the merchant's actual
stored region) are BUILD now.

---

## 12. Out of this pass entirely

Per your instruction, this pass ends at a working `docker compose up`. Build-plan phases 0 and
8–10 — platform facts, OpenChoreo onboarding, four environments, the observability plane —
come after. `deploy/` stays empty until then, and `docs/PLATFORM-FACTS.md` is written from a
live cluster, never from memory.

Also not built, from `README.md`'s non-goals: merchant onboarding, KYC, disputes, chargebacks,
FX, payment initiation, mobile app, unit-test coverage targets. Tests cover the money paths
only — idempotency, sum-zero, float limit, settlement atomicity, ledger balance.
