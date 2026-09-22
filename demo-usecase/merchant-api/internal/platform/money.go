package platform

import (
	"fmt"
	"strconv"
)

// Money is integer minor units, always. Never a float (README.md "Money").
// KES and NGN both have two minor digits, so 1 unit = 1/100 of the major unit.
type Minor int64

const minorDigits = 2

// String renders for humans and for log/error messages only - never for storage.
func (m Minor) String() string {
	neg := m < 0
	v := int64(m)
	if neg {
		v = -v
	}
	major := v / 100
	frac := v % 100
	s := strconv.FormatInt(major, 10) + "." + fmt.Sprintf("%02d", frac)
	if neg {
		return "-" + s
	}
	return s
}

// Channel is a payment method integration.
//
// C-1.2 lists four channels; the README's non-goals cap us at two, one per market
// (SCOPE.md conflict 1). The type is closed so an unsupported channel is a 400 at the
// edge rather than a bad row in the datastore.
type Channel string

const (
	ChannelMpesa         Channel = "mpesa"          // Kenya
	ChannelNIBSSTransfer Channel = "nibss_transfer" // Nigeria
)

type Currency string

const (
	KES Currency = "KES"
	NGN Currency = "NGN"
)

type Country string

const (
	KE Country = "KE"
	NG Country = "NG"
)

type channelSpec struct {
	Currency Currency
	Country  Country
}

var channels = map[Channel]channelSpec{
	ChannelMpesa:         {Currency: KES, Country: KE},
	ChannelNIBSSTransfer: {Currency: NGN, Country: NG},
}

func SupportedChannels() []Channel {
	return []Channel{ChannelMpesa, ChannelNIBSSTransfer}
}

// CurrencyForChannel implements C-1.8: currency is derived from the channel and is never
// accepted from a caller. The bool is false for an unsupported channel.
func CurrencyForChannel(c Channel) (Currency, bool) {
	s, ok := channels[c]
	return s.Currency, ok
}

// CountryForChannel gives the origin country (C-1.4), used for residency (RES-1).
func CountryForChannel(c Channel) (Country, bool) {
	s, ok := channels[c]
	return s.Country, ok
}

func ValidChannel(c Channel) bool {
	_, ok := channels[c]
	return ok
}

// FeeFor computes a fee from a per-channel percentage in basis points plus a fixed
// component (C-2.8). Basis points keep this integer arithmetic end to end - no float
// ever touches a money value. Rounding is half-up on the percentage part.
func FeeFor(gross Minor, percentageBP int, fixed Minor) Minor {
	if gross <= 0 {
		return fixed
	}
	pct := (int64(gross)*int64(percentageBP) + 5000) / 10000
	return Minor(pct) + fixed
}
