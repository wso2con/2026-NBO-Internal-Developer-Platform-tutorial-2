package store

import (
	"time"

	"github.com/mopay/collections-api/internal/platform"
)

const (
	StatusPending = "pending"
	StatusCleared = "cleared"
	StatusFailed  = "failed"
	StatusSettled = "settled"
)

type Merchant struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Country            string            `json:"country"`
	DataRegion         string            `json:"dataRegion"`
	PayoutBank         string            `json:"payoutBank"`
	PayoutAccountName  string            `json:"payoutAccountName"`
	PayoutAccountMask  string            `json:"payoutAccountNumberMasked"`
	FloatLimitMinor    platform.Minor    `json:"floatLimitMinor"`
	FloatLimitCurrency platform.Currency `json:"floatLimitCurrency"`
	CreatedAt          time.Time         `json:"createdAt"`
}

// Collection is PRD §7's Collection.
type Collection struct {
	ID                string            `json:"id"`
	MerchantID        string            `json:"merchantId"`
	Channel           platform.Channel  `json:"channel"`
	AmountMinor       platform.Minor    `json:"amountMinor"`
	Currency          platform.Currency `json:"currency"`
	CustomerReference string            `json:"customerReference,omitempty"`
	MerchantReference string            `json:"merchantReference"`
	OriginCountry     string            `json:"originCountry"`
	Status            string            `json:"status"`
	CreatedAt         time.Time         `json:"createdAt"`
	ClearedAt         *time.Time        `json:"clearedAt,omitempty"`
	SettledAt         *time.Time        `json:"settledAt,omitempty"`
	SettlementLineID  *string           `json:"settlementLineId,omitempty"`
}

// Redact removes customer-level fields a role may not see.
//
// C-6.3: an operations user must not be able to retrieve per-transaction customer
// references or amounts through ANY endpoint. C-6.4: finance sees no customer detail.
// This is applied in the store/API layer, not the console - C-6.6 is explicit that
// navigation hiding a screen is not a control.
func (c Collection) Redact(role platform.Role) Collection {
	if role.CanSeeCustomerDetail() {
		return c
	}
	c.CustomerReference = ""
	c.AmountMinor = 0
	return c
}

type SettlementRun struct {
	ID            string     `json:"id"`
	RunType       string     `json:"runType"`
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"startedAt"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
	RowCount      int64      `json:"rowCount"`
	FailureReason *string    `json:"failureReason,omitempty"`
	PeriodStart   time.Time  `json:"periodStart"`
	PeriodEnd     time.Time  `json:"periodEnd"`
	Region        string     `json:"region"`

	// Populated for merchant-scoped listings (C-1.13).
	NetMinor *platform.Minor    `json:"netMinor,omitempty"`
	Currency *platform.Currency `json:"currency,omitempty"`
}

// ElapsedSeconds drives S-3.3 (elapsed against the two-hour window).
func (r SettlementRun) ElapsedSeconds() float64 {
	end := time.Now().UTC()
	if r.EndedAt != nil {
		end = *r.EndedAt
	}
	return end.Sub(r.StartedAt).Seconds()
}

// ChannelAggregate is one row of C-1.12's per-channel split.
type ChannelAggregate struct {
	Channel     platform.Channel `json:"channel"`
	Count       int64            `json:"count"`
	ValueMinor  platform.Minor   `json:"valueMinor"`
	FailedCount int64            `json:"failedCount"`
}

type Aggregates struct {
	MerchantID  string             `json:"merchantId"`
	From        time.Time          `json:"from"`
	To          time.Time          `json:"to"`
	Count       int64              `json:"count"`
	ValueMinor  platform.Minor     `json:"valueMinor"`
	FailedCount int64              `json:"failedCount"`
	SuccessRate float64            `json:"successRate"`
	Currency    platform.Currency  `json:"currency"`
	ByChannel   []ChannelAggregate `json:"byChannel"`
	Hourly      []HourlyPoint      `json:"hourly"`
}

// HourlyPoint feeds S-1's 24-hour sparkline.
type HourlyPoint struct {
	Hour       time.Time      `json:"hour"`
	Count      int64          `json:"count"`
	ValueMinor platform.Minor `json:"valueMinor"`
}

// StatementLine is one channel's contribution to a monthly statement (C-1.14).
type StatementLine struct {
	Channel    platform.Channel `json:"channel"`
	Count      int64            `json:"count"`
	GrossMinor platform.Minor   `json:"grossMinor"`
	FeesMinor  platform.Minor   `json:"feesMinor"`
	NetMinor   platform.Minor   `json:"netMinor"`
}

// Statement is JSON only - README.md lists CSV export as a non-goal (SCOPE.md conflict 3).
type Statement struct {
	MerchantID   string            `json:"merchantId"`
	MerchantName string            `json:"merchantName"`
	Month        string            `json:"month"`
	PeriodStart  time.Time         `json:"periodStart"`
	PeriodEnd    time.Time         `json:"periodEnd"`
	Currency     platform.Currency `json:"currency"`
	GrossMinor   platform.Minor    `json:"grossMinor"`
	FeesMinor    platform.Minor    `json:"feesMinor"`
	NetMinor     platform.Minor    `json:"netMinor"`
	RowCount     int64             `json:"rowCount"`
	ByChannel    []StatementLine   `json:"byChannel"`
	RunIDs       []string          `json:"runIds"`
}

// SettlementStatus is the continuously-updated signal behind OBS-4, C-5.2 and S-3.4.
type SettlementStatus struct {
	Region             string     `json:"region"`
	LagSeconds         float64    `json:"lagSeconds"`
	OldestUnsettledAt  *time.Time `json:"oldestUnsettledAt"`
	LastCompletedRunAt *time.Time `json:"lastCompletedRunAt"`
	LastCompletedRunID *string    `json:"lastCompletedRunId"`
	StalenessThreshold float64    `json:"stalenessThresholdSeconds"`
	Stale              bool       `json:"stale"`
	UnsettledCount     int64      `json:"unsettledCount"`
	AffectedMerchants  int64      `json:"affectedMerchants"`
}
