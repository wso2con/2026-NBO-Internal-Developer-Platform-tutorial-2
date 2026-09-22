package store

import (
	"time"

	"github.com/mopay/merchant-api/internal/platform"
)

// FeeSchedule is what the platform charges one merchant on one channel (per merchant AND
// per channel). percentageBp is basis points as an integer - fee arithmetic is never done
// in floating point.
type FeeSchedule struct {
	Channel      platform.Channel `json:"channel"`
	PercentageBP int              `json:"percentageBp"`
	FixedMinor   platform.Minor   `json:"fixedMinor"`
}

// Merchant is the identity record. PayoutAccountMask is what reads return; the full
// account number is accepted at onboarding and never leaves the datastore afterwards.
type Merchant struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Country            string            `json:"country"`
	DataRegion         string            `json:"dataRegion"`
	PayoutBank         string            `json:"payoutBank"`
	PayoutAccountMask  string            `json:"payoutAccountMask"`
	PayoutAccountName  string            `json:"payoutAccountName"`
	FloatLimitMinor    platform.Minor    `json:"floatLimitMinor"`
	FloatLimitCurrency platform.Currency `json:"floatLimitCurrency"`
	FeeSchedules       []FeeSchedule     `json:"feeSchedules"`
	CreatedAt          time.Time         `json:"createdAt"`
}

// OnboardRequest is the validated onboarding payload. The id is assigned by this service,
// never supplied by the caller.
type OnboardRequest struct {
	Name                string            `json:"name"`
	Country             string            `json:"country"`
	DataRegion          string            `json:"dataRegion"`
	PayoutBank          string            `json:"payoutBank"`
	PayoutAccountNumber string            `json:"payoutAccountNumber"`
	PayoutAccountName   string            `json:"payoutAccountName"`
	FloatLimitMinor     platform.Minor    `json:"floatLimitMinor"`
	FloatLimitCurrency  platform.Currency `json:"floatLimitCurrency"`
	FeeSchedules        []FeeSchedule     `json:"feeSchedules"`
}
