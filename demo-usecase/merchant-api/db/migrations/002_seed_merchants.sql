-- The PRD §12 development dataset: 12 merchants, 7 Kenyan and 5 Nigerian.
--
-- These identities are the ones mopay's existing collections already reference, so they
-- are seeded here with the same ids, float limits and fee terms. mopay derives its float
-- limits deterministically at seed time, so these values survive a `make reset` there.
--
-- mopay's internal `mopay_house` accounting account is deliberately NOT here: it is not
-- a merchant and is never onboarded.

INSERT INTO merchants (id, name, country, data_region, payout_bank,
                       payout_account_number, payout_account_name,
                       float_limit_minor, float_limit_currency) VALUES
    ('mch_001', 'Nakuru Fresh Produce', 'KE', 'KE', 'Equity Bank Kenya', '1000000000', 'Nakuru Fresh Produce', 104877000, 'KES'),
    ('mch_002', 'Mombasa Water Utility', 'KE', 'KE', 'KCB Bank Kenya', '1000007919', 'Mombasa Water Utility', 176323500, 'KES'),
    ('mch_003', 'Riverside Academy', 'KE', 'KE', 'Co-operative Bank', '1000015838', 'Riverside Academy', 29003250, 'KES'),
    ('mch_004', 'Thika Road Hardware', 'KE', 'KE', 'Absa Bank Kenya', '1000023757', 'Thika Road Hardware', 18739542, 'KES'),
    ('mch_005', 'Kisumu Pharmacy Group', 'KE', 'KE', 'NCBA Bank Kenya', '1000031676', 'Kisumu Pharmacy Group', 61916400, 'KES'),
    ('mch_006', 'Eldoret Grain Millers', 'KE', 'KE', 'Stanbic Bank Kenya', '1000039595', 'Eldoret Grain Millers', 114087500, 'KES'),
    ('mch_007', 'Karen Veterinary Clinic', 'KE', 'KE', 'Family Bank', '1000047514', 'Karen Veterinary Clinic', 27394920, 'KES'),
    ('mch_008', 'Lekki Power Distribution', 'NG', 'NG', 'Guaranty Trust Bank', '1000055433', 'Lekki Power Distribution', 1500616000, 'NGN'),
    ('mch_009', 'Ikeja Electronics Market', 'NG', 'NG', 'Zenith Bank', '1000063352', 'Ikeja Electronics Market', 697051520, 'NGN'),
    ('mch_010', 'Abuja International School', 'NG', 'NG', 'Access Bank', '1000071271', 'Abuja International School', 271011000, 'NGN'),
    ('mch_011', 'Port Harcourt Logistics', 'NG', 'NG', 'First Bank of Nigeria', '1000079190', 'Port Harcourt Logistics', 149805972, 'NGN'),
    ('mch_012', 'Kano Textile Wholesalers', 'NG', 'NG', 'United Bank for Africa', '1000087109', 'Kano Textile Wholesalers', 1109345020, 'NGN')
ON CONFLICT (id) DO NOTHING;

INSERT INTO fee_schedules (merchant_id, channel, percentage_bp, fixed_minor) VALUES
    ('mch_001', 'mpesa', 150, 1000),
    ('mch_002', 'mpesa', 90, 500),
    ('mch_003', 'mpesa', 120, 2000),
    ('mch_004', 'mpesa', 175, 1500),
    ('mch_005', 'mpesa', 135, 1000),
    ('mch_006', 'mpesa', 100, 2500),
    ('mch_007', 'mpesa', 200, 500),
    ('mch_008', 'nibss_transfer', 110, 5000),
    ('mch_009', 'nibss_transfer', 185, 2500),
    ('mch_010', 'nibss_transfer', 125, 10000),
    ('mch_011', 'nibss_transfer', 160, 3000),
    ('mch_012', 'nibss_transfer', 145, 2000)
ON CONFLICT (merchant_id, channel) DO NOTHING;
