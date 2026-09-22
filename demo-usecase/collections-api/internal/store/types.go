package store

import (
	"time"

	"github.com/mopay/collections-api/internal/platform"
)

// Request types for the ledger data surface.
//
// These are declared identically in ledger/internal/collections, and travel over the wire
// using Go's default field-name marshalling - the two definitions must stay in step.

type CreateCollectionRequest struct {
	MerchantID        string
	Channel           platform.Channel
	AmountMinor       platform.Minor
	CustomerReference string
	MerchantReference string
}

type SearchParams struct {
	MerchantID        string
	MerchantReference string
	Status            string
	From              *time.Time
	To                *time.Time
	Limit             int
	Offset            int
}

// ProjectedMerchant is merchant-api's view of a merchant, as mopay records it.
type ProjectedMerchant struct {
	ID                 string
	Name               string
	Country            string
	DataRegion         string
	PayoutBank         string
	PayoutAccountRef   string
	PayoutAccountName  string
	FloatLimitMinor    platform.Minor
	FloatLimitCurrency platform.Currency
	FeeSchedules       []ProjectedFeeSchedule
}

type ProjectedFeeSchedule struct {
	Channel      platform.Channel
	PercentageBP int
	FixedMinor   platform.Minor
}
