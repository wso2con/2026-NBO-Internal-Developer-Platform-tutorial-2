-- The ledger's counterparty account.
--
-- PRD §7 gives LedgerEntry no `account` column: an entry is identified by merchant_id plus
-- direction. Double entry still needs both sides of every transaction, so mopay's own book
-- is represented as a reserved merchant row. This is structural, not seed data - the ledger
-- cannot write a balancing pair without it, so it lives in a migration.
--
-- Sign convention (balance = SUM(credit) - SUM(debit) per merchant):
--   collection cleared : credit merchant, debit house   -> we owe the merchant more
--   fee charged        : debit  merchant, credit house  -> we owe less
--   settlement paid    : debit  merchant, credit house  -> we owe less
-- A fully settled merchant therefore returns to a zero balance.

\set ON_ERROR_STOP on

INSERT INTO merchants (
    id, name, country, data_region,
    payout_bank, payout_account_number, payout_account_name,
    float_limit_minor, float_limit_currency
) VALUES (
    :'house_id',
    'mopay house account',
    :'region',
    :'region',
    'n/a', 'n/a', 'n/a',
    0,
    CASE :'region' WHEN 'KE' THEN 'KES' ELSE 'NGN' END
)
ON CONFLICT (id) DO NOTHING;
