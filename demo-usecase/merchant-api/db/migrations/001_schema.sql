-- merchant-api owns merchant identity. This is a SEPARATE datastore from mopay's:
-- separate instance, separate credentials, no shared volume and no cross-database query
-- path. mopay holds a projection of these rows, never the other way round.

CREATE TABLE IF NOT EXISTS merchants (
    id                     text PRIMARY KEY,
    name                   text        NOT NULL,
    country                text        NOT NULL CHECK (country IN ('KE','NG')),
    -- Immutable after creation, and enforced rather than documented: moving a merchant
    -- between regions would move transaction data across a boundary required to stay
    -- sealed. There is deliberately no API operation that changes it.
    data_region            text        NOT NULL CHECK (data_region IN ('KE','NG')),
    payout_bank            text        NOT NULL,
    payout_account_number  text        NOT NULL,
    payout_account_name    text        NOT NULL,
    float_limit_minor      bigint      NOT NULL CHECK (float_limit_minor >= 0),
    float_limit_currency   text        NOT NULL CHECK (float_limit_currency IN ('KES','NGN')),
    created_at             timestamptz NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION merchants_data_region_immutable() RETURNS trigger AS $$
BEGIN
    IF NEW.data_region IS DISTINCT FROM OLD.data_region THEN
        RAISE EXCEPTION
            'merchant % data_region is immutable: attempted change from % to %',
            OLD.id, OLD.data_region, NEW.data_region;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_merchants_data_region_immutable ON merchants;
CREATE TRIGGER trg_merchants_data_region_immutable
    BEFORE UPDATE ON merchants
    FOR EACH ROW EXECUTE FUNCTION merchants_data_region_immutable();

-- Per merchant AND per channel. Basis points as an integer - fee arithmetic is never
-- done in floating point.
CREATE TABLE IF NOT EXISTS fee_schedules (
    merchant_id    text   NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    channel        text   NOT NULL CHECK (channel IN ('mpesa','nibss_transfer')),
    percentage_bp  int    NOT NULL CHECK (percentage_bp >= 0 AND percentage_bp <= 10000),
    fixed_minor    bigint NOT NULL CHECK (fixed_minor >= 0),
    PRIMARY KEY (merchant_id, channel)
);

CREATE INDEX IF NOT EXISTS idx_merchants_region_created
    ON merchants (data_region, created_at DESC);
