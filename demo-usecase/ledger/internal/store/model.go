package store

import (
	"time"

	"github.com/mopay/ledger/internal/platform"
)

// Entry is one side of a double-entry pair (PRD §7 LedgerEntry).
type Entry struct {
	ID                 string            `json:"id"`
	MerchantID         string            `json:"merchantId"`
	Direction          string            `json:"direction"` // debit | credit
	AmountMinor        platform.Minor    `json:"amountMinor"`
	Currency           platform.Currency `json:"currency"`
	CreatedAt          time.Time         `json:"createdAt"`
	EventType          string            `json:"eventType"`
	EventID            string            `json:"eventId"`
	TransactionGroupID string            `json:"transactionGroupId"`
}

const (
	DirectionDebit  = "debit"
	DirectionCredit = "credit"

	EventCollection = "collection"
	EventFee        = "fee"
	EventSettlement = "settlement"
	EventCorrection = "correction"
)

// Signed returns the entry's contribution to a balance: credit positive, debit negative.
// C-3.5's sum-zero check and C-3.2's balance both use this one definition.
func (e Entry) Signed() platform.Minor {
	if e.Direction == DirectionDebit {
		return -e.AmountMinor
	}
	return e.AmountMinor
}

// EntryGroup is an atomic, balancing set of entries sharing a transactionGroupId.
type EntryGroup struct {
	TransactionGroupID string  `json:"transactionGroupId"`
	EventType          string  `json:"eventType"`
	EventID            string  `json:"eventId"`
	Entries            []Entry `json:"entries"`
}

// Balance is a merchant's position (C-3.2).
type Balance struct {
	MerchantID  string            `json:"merchantId"`
	AmountMinor platform.Minor    `json:"amountMinor"`
	Currency    platform.Currency `json:"currency"`
	AsOf        time.Time         `json:"asOf"`
	EntryCount  int64             `json:"entryCount"`
}

// FeeSchedule is a merchant's per-channel fee (C-2.8).
type FeeSchedule struct {
	Channel      platform.Channel `json:"channel"`
	PercentageBP int              `json:"percentageBp"`
	FixedMinor   platform.Minor   `json:"fixedMinor"`
}

// PendingCollection is one element of the unpaginated pending-settlements read.
// Per hard constraint 3 it carries everything a settlement run needs for that collection,
// so a full-period reconciliation needs no follow-up calls.
type PendingCollection struct {
	CollectionID      string            `json:"collectionId"`
	MerchantID        string            `json:"merchantId"`
	MerchantName      string            `json:"merchantName"`
	Channel           platform.Channel  `json:"channel"`
	AmountMinor       platform.Minor    `json:"amountMinor"`
	Currency          platform.Currency `json:"currency"`
	MerchantReference string            `json:"merchantReference"`
	OriginCountry     string            `json:"originCountry"`
	CreatedAt         time.Time         `json:"createdAt"`
	ClearedAt         *time.Time        `json:"clearedAt"`
	LedgerEntries     []Entry           `json:"ledgerEntries"`
	FeeSchedule       *FeeSchedule      `json:"feeSchedule"`
	PayoutBank        string            `json:"payoutBank"`
	PayoutAccountName string            `json:"payoutAccountName"`
}

// SettlementLine is immutable once written (C-2.3, NFR-6).
type SettlementLine struct {
	ID             string            `json:"id"`
	RunID          string            `json:"runId"`
	MerchantID     string            `json:"merchantId"`
	GrossMinor     platform.Minor    `json:"grossMinor"`
	FeesMinor      platform.Minor    `json:"feesMinor"`
	NetMinor       platform.Minor    `json:"netMinor"`
	Currency       platform.Currency `json:"currency"`
	CorrectsLineID *string           `json:"correctsLineId"`
	RowCount       int64             `json:"rowCount"`
	CreatedAt      time.Time         `json:"createdAt"`
}

// CommitRequest settles one merchant for one run. It is deliberately whole-merchant:
// C-2.6 requires that a failure affecting one merchant leaves no other merchant
// partially settled, so the line, the payout instruction, the fee and settlement ledger
// entries, and the collection status updates all commit in a single transaction.
type CommitRequest struct {
	RunID         string            `json:"runId"`
	MerchantID    string            `json:"merchantId"`
	Channel       platform.Channel  `json:"channel"`
	CollectionIDs []string          `json:"collectionIds"`
	GrossMinor    platform.Minor    `json:"grossMinor"`
	FeesMinor     platform.Minor    `json:"feesMinor"`
	NetMinor      platform.Minor    `json:"netMinor"`
	Currency      platform.Currency `json:"currency"`
	SettledAt     time.Time         `json:"settledAt"`
}

// CommitResult reports what the commit did. AlreadySettled is true when the run had
// already settled this merchant (C-2.7 - re-running must not double-settle).
type CommitResult struct {
	Line           SettlementLine `json:"line"`
	AlreadySettled bool           `json:"alreadySettled"`
	PayoutID       string         `json:"payoutId"`
}
