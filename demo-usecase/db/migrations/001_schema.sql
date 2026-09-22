-- mopay schema. Money is ALWAYS integer minor units (bigint). Currency always explicit.
-- All timestamps are timestamptz, stored UTC. See PRD §7.

CREATE TABLE IF NOT EXISTS merchants (
    id                     text PRIMARY KEY,
    name                   text        NOT NULL,
    country                text        NOT NULL CHECK (country IN ('KE','NG')),
    -- RES-5: immutable after creation. Enforced by trigger below.
    data_region            text        NOT NULL CHECK (data_region IN ('KE','NG')),
    payout_bank            text        NOT NULL,
    payout_account_number  text        NOT NULL,
    payout_account_name    text        NOT NULL,
    float_limit_minor      bigint      NOT NULL CHECK (float_limit_minor >= 0),
    float_limit_currency   text        NOT NULL,
    created_at             timestamptz NOT NULL DEFAULT now()
);

-- RES-5 / §7: Merchant.dataRegion is set at creation and immutable.
CREATE OR REPLACE FUNCTION merchants_data_region_immutable() RETURNS trigger AS $$
BEGIN
    IF NEW.data_region IS DISTINCT FROM OLD.data_region THEN
        RAISE EXCEPTION
            'merchant % data_region is immutable (RES-5): attempted change from % to %',
            OLD.id, OLD.data_region, NEW.data_region;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_merchants_data_region_immutable ON merchants;
CREATE TRIGGER trg_merchants_data_region_immutable
    BEFORE UPDATE ON merchants
    FOR EACH ROW EXECUTE FUNCTION merchants_data_region_immutable();

-- C-2.8: per-merchant, per-channel percentage (basis points, integer - never a float)
-- plus a fixed component in minor units.
CREATE TABLE IF NOT EXISTS fee_schedules (
    merchant_id    text   NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    channel        text   NOT NULL,
    percentage_bp  int    NOT NULL CHECK (percentage_bp >= 0 AND percentage_bp <= 10000),
    fixed_minor    bigint NOT NULL CHECK (fixed_minor >= 0),
    PRIMARY KEY (merchant_id, channel)
);

CREATE TABLE IF NOT EXISTS collections (
    id                  text        PRIMARY KEY,
    merchant_id         text        NOT NULL REFERENCES merchants(id),
    channel             text        NOT NULL CHECK (channel IN ('mpesa','nibss_transfer')),
    amount_minor        bigint      NOT NULL CHECK (amount_minor > 0),
    currency            text        NOT NULL CHECK (currency IN ('KES','NGN')),
    customer_reference  text        NOT NULL,
    merchant_reference  text        NOT NULL,
    origin_country      text        NOT NULL CHECK (origin_country IN ('KE','NG')),
    status              text        NOT NULL CHECK (status IN ('pending','cleared','failed','settled')),
    created_at          timestamptz NOT NULL DEFAULT now(),
    cleared_at          timestamptz,
    settled_at          timestamptz,
    settlement_line_id  text,
    -- C-1.3: idempotency is a database constraint, not an application-level check.
    CONSTRAINT collections_merchant_reference_unique UNIQUE (merchant_id, merchant_reference)
);

CREATE INDEX IF NOT EXISTS idx_collections_merchant_created ON collections (merchant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_collections_status_cleared   ON collections (status, cleared_at);
CREATE INDEX IF NOT EXISTS idx_collections_merchant_status  ON collections (merchant_id, status);

-- C-1.6: status transitions are one-directional. pending -> cleared -> settled, or pending -> failed.
CREATE OR REPLACE FUNCTION collections_status_forward_only() RETURNS trigger AS $$
DECLARE
    rank_old int;
    rank_new int;
BEGIN
    rank_old := CASE OLD.status WHEN 'pending' THEN 0 WHEN 'cleared' THEN 1
                                WHEN 'failed'  THEN 2 WHEN 'settled' THEN 2 END;
    rank_new := CASE NEW.status WHEN 'pending' THEN 0 WHEN 'cleared' THEN 1
                                WHEN 'failed'  THEN 2 WHEN 'settled' THEN 2 END;
    IF NEW.status IS DISTINCT FROM OLD.status THEN
        IF rank_new < rank_old THEN
            RAISE EXCEPTION
                'collection % illegal status transition % -> % (C-1.6: transitions are one-directional)',
                OLD.id, OLD.status, NEW.status;
        END IF;
        IF OLD.status = 'failed' OR OLD.status = 'settled' THEN
            RAISE EXCEPTION
                'collection % is terminal in status % and cannot move to % (C-1.6)',
                OLD.id, OLD.status, NEW.status;
        END IF;
        IF OLD.status = 'pending' AND NEW.status = 'settled' THEN
            RAISE EXCEPTION
                'collection % cannot settle directly from pending (C-1.6: must clear first)',
                OLD.id;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_collections_status_forward_only ON collections;
CREATE TRIGGER trg_collections_status_forward_only
    BEFORE UPDATE ON collections
    FOR EACH ROW EXECUTE FUNCTION collections_status_forward_only();

-- C-2.5: full run record.
CREATE TABLE IF NOT EXISTS settlement_runs (
    id              text        PRIMARY KEY,
    run_type        text        NOT NULL CHECK (run_type IN ('nightly','monthEnd')),
    status          text        NOT NULL CHECK (status IN ('pending','running','completed','failed')),
    started_at      timestamptz NOT NULL,
    ended_at        timestamptz,
    row_count       bigint      NOT NULL DEFAULT 0,
    failure_reason  text,
    period_start    timestamptz NOT NULL,
    period_end      timestamptz NOT NULL,
    region          text        NOT NULL CHECK (region IN ('KE','NG'))
);

CREATE INDEX IF NOT EXISTS idx_runs_started ON settlement_runs (started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_status  ON settlement_runs (status, started_at DESC);

-- C-2.3 / NFR-6: immutable. UPDATE and DELETE are revoked from the app role in 002.
CREATE TABLE IF NOT EXISTS settlement_lines (
    id               text        PRIMARY KEY,
    run_id           text        NOT NULL REFERENCES settlement_runs(id),
    merchant_id      text        NOT NULL REFERENCES merchants(id),
    gross_minor      bigint      NOT NULL,
    fees_minor       bigint      NOT NULL,
    net_minor        bigint      NOT NULL,
    currency         text        NOT NULL,
    -- C-2.3: corrections would reference the line they correct. SCOPE.md conflict 6 -
    -- the column exists and is always NULL; there is no correction code path.
    corrects_line_id text        REFERENCES settlement_lines(id),
    row_count        bigint      NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- C-2.7: one line per merchant per run. Re-running cannot double-write.
    CONSTRAINT settlement_lines_run_merchant_unique UNIQUE (run_id, merchant_id)
);

CREATE INDEX IF NOT EXISTS idx_lines_merchant ON settlement_lines (merchant_id, created_at DESC);

-- C-2.4: one payout instruction per merchant per completed run.
CREATE TABLE IF NOT EXISTS payout_instructions (
    id                 text        PRIMARY KEY,
    settlement_line_id text        NOT NULL UNIQUE REFERENCES settlement_lines(id),
    merchant_id        text        NOT NULL REFERENCES merchants(id),
    bank               text        NOT NULL,
    account_number     text        NOT NULL,
    account_name       text        NOT NULL,
    amount_minor       bigint      NOT NULL,
    currency           text        NOT NULL,
    status             text        NOT NULL DEFAULT 'emitted',
    created_at         timestamptz NOT NULL DEFAULT now()
);

-- C-3.3 / hard constraint 1: append-only. UPDATE and DELETE revoked in 002.
CREATE TABLE IF NOT EXISTS ledger_entries (
    id                   text        PRIMARY KEY,
    merchant_id          text        NOT NULL REFERENCES merchants(id),
    direction            text        NOT NULL CHECK (direction IN ('debit','credit')),
    amount_minor         bigint      NOT NULL CHECK (amount_minor > 0),
    currency             text        NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    -- C-3.4: every entry references the business event that produced it.
    event_type           text        NOT NULL CHECK (event_type IN ('collection','fee','settlement','correction')),
    event_id             text        NOT NULL,
    -- C-3.5: entries sharing this must sum to zero.
    transaction_group_id text        NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ledger_merchant_created ON ledger_entries (merchant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_ledger_group            ON ledger_entries (transaction_group_id);
CREATE INDEX IF NOT EXISTS idx_ledger_event            ON ledger_entries (event_type, event_id);
