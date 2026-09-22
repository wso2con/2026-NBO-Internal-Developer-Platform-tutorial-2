package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mopay/ledger/internal/platform"
)

// PendingSettlements returns EVERY cleared, unsettled collection in the period for the
// region, each carrying its ledger entries, the merchant's fee schedule and channel
// metadata.
//
// This read is UNPAGINATED BY DESIGN - see README hard constraint 3. It is the honest
// read path for full-period reconciliation (PRD C-2.2): a period you can only see one page
// of cannot be reconciled. There is no limit, no offset, no cursor and no result cap, and
// none should be added. The caller is expected to hold the whole set.
//
// Residency (RES-1): the region filter is applied to the merchant's data_region, so a run
// in one region can never pull the other region's transaction rows.
func (s *Store) PendingSettlements(ctx context.Context, region string, periodStart, periodEnd time.Time) ([]PendingCollection, error) {
	if region != "KE" && region != "NG" {
		return nil, platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"region %q is not a data region (want KE or NG)", region)
	}
	if !periodEnd.After(periodStart) {
		return nil, platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"periodEnd %s must be after periodStart %s",
			periodEnd.Format(time.RFC3339), periodStart.Format(time.RFC3339))
	}

	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.merchant_id, m.name, c.channel, c.amount_minor, c.currency,
		       c.merchant_reference, c.origin_country, c.created_at, c.cleared_at,
		       f.percentage_bp, f.fixed_minor,
		       m.payout_bank, m.payout_account_name
		  FROM collections c
		  JOIN merchants m ON m.id = c.merchant_id
		  LEFT JOIN fee_schedules f ON f.merchant_id = c.merchant_id AND f.channel = c.channel
		 WHERE c.status = 'cleared'
		   AND m.data_region = $1
		   AND c.cleared_at >= $2 AND c.cleared_at < $3
		 ORDER BY c.merchant_id, c.cleared_at, c.id`,
		region, periodStart.UTC(), periodEnd.UTC())
	if err != nil {
		return nil, fmt.Errorf("read pending settlements for region %s period %s..%s: %w",
			region, periodStart.Format(time.RFC3339), periodEnd.Format(time.RFC3339), err)
	}
	defer rows.Close()

	out := []PendingCollection{}
	ids := []string{}
	for rows.Next() {
		var p PendingCollection
		var amt int64
		var channel, currency string
		var pctBP *int
		var fixed *int64
		if err := rows.Scan(&p.CollectionID, &p.MerchantID, &p.MerchantName, &channel, &amt, &currency,
			&p.MerchantReference, &p.OriginCountry, &p.CreatedAt, &p.ClearedAt,
			&pctBP, &fixed, &p.PayoutBank, &p.PayoutAccountName); err != nil {
			return nil, fmt.Errorf("scan pending settlement row for region %s: %w", region, err)
		}
		p.Channel = platform.Channel(channel)
		p.Currency = platform.Currency(currency)
		p.AmountMinor = platform.Minor(amt)
		if pctBP != nil && fixed != nil {
			p.FeeSchedule = &FeeSchedule{
				Channel: p.Channel, PercentageBP: *pctBP, FixedMinor: platform.Minor(*fixed),
			}
		}
		out = append(out, p)
		ids = append(ids, p.CollectionID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending settlements for region %s: %w", region, err)
	}
	if len(out) == 0 {
		return out, nil
	}

	// Attach ledger entries. One query for the whole set rather than one per collection -
	// unpaginated does not have to mean N+1.
	entryRows, err := s.pool.Query(ctx, `
		SELECT id, merchant_id, direction, amount_minor, currency, created_at,
		       event_type, event_id, transaction_group_id
		  FROM ledger_entries
		 WHERE event_type = 'collection' AND event_id = ANY($1)
		 ORDER BY event_id, id`, ids)
	if err != nil {
		return nil, fmt.Errorf("read ledger entries for %d pending collections in region %s: %w",
			len(ids), region, err)
	}
	defer entryRows.Close()

	byCollection := map[string][]Entry{}
	entries, err := scanEntries(entryRows, "pending settlements")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		byCollection[e.EventID] = append(byCollection[e.EventID], e)
	}
	for i := range out {
		out[i].LedgerEntries = byCollection[out[i].CollectionID]
	}
	return out, nil
}

// CommitSettlement settles ONE merchant for one run, in ONE transaction.
//
// C-2.6: a failure affecting one merchant must not leave another merchant partially
// settled. That is why the unit of work here is the merchant and not the run, and why the
// settlement line, the payout instruction, the fee and settlement ledger entries and the
// collection status updates all commit or roll back together.
//
// C-2.7: re-running a period must not double-settle. Idempotency is keyed on the
// collection - the UPDATE only touches rows still in 'cleared', and the unique constraint
// on (run_id, merchant_id) stops a second line for the same run.
func (s *Store) CommitSettlement(ctx context.Context, req CommitRequest) (CommitResult, error) {
	var res CommitResult

	if req.RunID == "" || req.MerchantID == "" {
		return res, platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"settlement commit requires runId and merchantId")
	}
	if req.NetMinor != req.GrossMinor-req.FeesMinor {
		return res, platform.Errorf(http.StatusUnprocessableEntity, platform.CodeUnprocessable,
			"settlement commit for run %s merchant %s is inconsistent: net %s != gross %s - fees %s",
			req.RunID, req.MerchantID, req.NetMinor, req.GrossMinor, req.FeesMinor)
	}
	if len(req.CollectionIDs) == 0 {
		return res, platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"settlement commit for run %s merchant %s carries no collection ids",
			req.RunID, req.MerchantID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin settlement commit for run %s merchant %s: %w",
			req.RunID, req.MerchantID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Already settled by an earlier attempt at this run? Return it unchanged (C-2.7).
	existing, err := lineForRunMerchant(ctx, tx, req.RunID, req.MerchantID)
	if err != nil {
		return res, err
	}
	if existing != nil {
		res.Line = *existing
		res.AlreadySettled = true
		return res, nil
	}

	settledAt := req.SettledAt.UTC()
	if settledAt.IsZero() {
		settledAt = time.Now().UTC()
	}

	lineID := newID("sl")
	_, err = tx.Exec(ctx, `
		INSERT INTO settlement_lines
		    (id, run_id, merchant_id, gross_minor, fees_minor, net_minor, currency,
		     corrects_line_id, row_count, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULL,$8,$9)`,
		lineID, req.RunID, req.MerchantID, int64(req.GrossMinor), int64(req.FeesMinor),
		int64(req.NetMinor), string(req.Currency), int64(len(req.CollectionIDs)), settledAt)
	if err != nil {
		if isPrivilegeError(err) {
			return res, platform.Errorf(http.StatusInternalServerError, platform.CodeAppendOnlyViolation,
				"settlement line insert refused by database for run %s merchant %s: %v",
				req.RunID, req.MerchantID, err)
		}
		return res, fmt.Errorf("insert settlement line for run %s merchant %s: %w",
			req.RunID, req.MerchantID, err)
	}

	// Mark exactly the collections that are still cleared. A collection already settled by
	// a previous run is left alone, which is what makes a re-run a no-op rather than a
	// double settlement (C-2.7).
	tag, err := tx.Exec(ctx, `
		UPDATE collections
		   SET status = 'settled', settled_at = $1, settlement_line_id = $2
		 WHERE id = ANY($3) AND merchant_id = $4 AND status = 'cleared'`,
		settledAt, lineID, req.CollectionIDs, req.MerchantID)
	if err != nil {
		return res, fmt.Errorf("mark %d collections settled for run %s merchant %s: %w",
			len(req.CollectionIDs), req.RunID, req.MerchantID, err)
	}
	if tag.RowsAffected() != int64(len(req.CollectionIDs)) {
		return res, platform.Errorf(http.StatusConflict, platform.CodeConflict,
			"settlement run %s for merchant %s expected to settle %d collections but %d were still cleared: another run settled them concurrently",
			req.RunID, req.MerchantID, len(req.CollectionIDs), tag.RowsAffected())
	}

	// Ledger: fee reduces what we owe, settlement discharges the rest. Both balance
	// against the house account (see migration 003).
	if req.FeesMinor > 0 {
		if err := insertGroupTx(ctx, tx, EntryGroup{
			TransactionGroupID: "tg_fee_" + lineID,
			EventType:          EventFee,
			EventID:            lineID,
			Entries: []Entry{
				{MerchantID: req.MerchantID, Direction: DirectionDebit, AmountMinor: req.FeesMinor, Currency: req.Currency},
				{MerchantID: s.houseAccountID, Direction: DirectionCredit, AmountMinor: req.FeesMinor, Currency: req.Currency},
			},
		}, settledAt); err != nil {
			return res, err
		}
	}
	if req.NetMinor > 0 {
		if err := insertGroupTx(ctx, tx, EntryGroup{
			TransactionGroupID: "tg_stl_" + lineID,
			EventType:          EventSettlement,
			EventID:            lineID,
			Entries: []Entry{
				{MerchantID: req.MerchantID, Direction: DirectionDebit, AmountMinor: req.NetMinor, Currency: req.Currency},
				{MerchantID: s.houseAccountID, Direction: DirectionCredit, AmountMinor: req.NetMinor, Currency: req.Currency},
			},
		}, settledAt); err != nil {
			return res, err
		}
	}

	// C-2.4: one payout instruction per merchant per completed run.
	payoutID := newID("po")
	_, err = tx.Exec(ctx, `
		INSERT INTO payout_instructions
		    (id, settlement_line_id, merchant_id, bank, account_number, account_name,
		     amount_minor, currency, status, created_at)
		SELECT $1, $2, m.id, m.payout_bank, m.payout_account_number, m.payout_account_name,
		       $3, $4, 'emitted', $5
		  FROM merchants m WHERE m.id = $6`,
		payoutID, lineID, int64(req.NetMinor), string(req.Currency), settledAt, req.MerchantID)
	if err != nil {
		return res, fmt.Errorf("emit payout instruction for run %s merchant %s: %w",
			req.RunID, req.MerchantID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit settlement for run %s merchant %s (%d collections, net %s %s): %w",
			req.RunID, req.MerchantID, len(req.CollectionIDs), req.NetMinor, req.Currency, err)
	}

	res.PayoutID = payoutID
	res.Line = SettlementLine{
		ID: lineID, RunID: req.RunID, MerchantID: req.MerchantID,
		GrossMinor: req.GrossMinor, FeesMinor: req.FeesMinor, NetMinor: req.NetMinor,
		Currency: req.Currency, RowCount: int64(len(req.CollectionIDs)), CreatedAt: settledAt,
	}
	return res, nil
}

func lineForRunMerchant(ctx context.Context, tx pgx.Tx, runID, merchantID string) (*SettlementLine, error) {
	var l SettlementLine
	var gross, fees, net, rowCount int64
	var cur string
	err := tx.QueryRow(ctx, `
		SELECT id, run_id, merchant_id, gross_minor, fees_minor, net_minor, currency,
		       corrects_line_id, row_count, created_at
		  FROM settlement_lines WHERE run_id = $1 AND merchant_id = $2`,
		runID, merchantID).Scan(&l.ID, &l.RunID, &l.MerchantID, &gross, &fees, &net, &cur,
		&l.CorrectsLineID, &rowCount, &l.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up settlement line for run %s merchant %s: %w", runID, merchantID, err)
	}
	l.GrossMinor = platform.Minor(gross)
	l.FeesMinor = platform.Minor(fees)
	l.NetMinor = platform.Minor(net)
	l.RowCount = rowCount
	l.Currency = platform.Currency(cur)
	return &l, nil
}

// insertGroupTx appends a validated group inside an existing transaction.
func insertGroupTx(ctx context.Context, tx pgx.Tx, g EntryGroup, at time.Time) error {
	if err := ValidateGroup(g); err != nil {
		return err
	}
	for _, e := range g.Entries {
		_, err := tx.Exec(ctx, `
			INSERT INTO ledger_entries
			    (id, merchant_id, direction, amount_minor, currency, created_at,
			     event_type, event_id, transaction_group_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			newID("le"), e.MerchantID, e.Direction, int64(e.AmountMinor), string(e.Currency),
			at, g.EventType, g.EventID, g.TransactionGroupID)
		if err != nil {
			if isPrivilegeError(err) {
				return platform.Errorf(http.StatusInternalServerError, platform.CodeAppendOnlyViolation,
					"ledger insert refused by database for group %s: %v", g.TransactionGroupID, err)
			}
			return fmt.Errorf("insert ledger entry for group %s merchant %s: %w",
				g.TransactionGroupID, e.MerchantID, err)
		}
	}
	return nil
}
