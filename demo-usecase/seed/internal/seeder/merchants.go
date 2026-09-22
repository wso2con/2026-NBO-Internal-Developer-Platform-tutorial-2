// Package seeder generates the development dataset in PRD §12.
//
// Deterministic with a fixed seed, so two runs produce identical data and a demo
// run matches the performance.
package seeder

import (
	"math/rand"

	"github.com/mopay/seed/internal/platform"
)

// PRD §12: 12 merchants, 7 Kenyan and 5 Nigerian, with varied fee schedules and float
// limits. Names are fictional.
type MerchantSpec struct {
	ID           string
	Name         string
	Country      platform.Country
	Channel      platform.Channel
	Currency     platform.Currency
	PercentageBP int
	FixedMinor   platform.Minor
	// FloatLimit is a fallback only. The real limit is derived from the merchant's
	// actual unsettled day-0 balance times HeadroomBP, because a limit that ignores
	// daily volume either blocks every collection or is never reachable.
	FloatLimit platform.Minor
	// HeadroomBP sizes the float limit against one day of unsettled volume, in basis
	// points: 30000 = 3x headroom. Varied per merchant for realism.
	HeadroomBP int
	// NearLimit marks the two merchants deliberately close to their float limit, so
	// C-1.5 is exercisable without setting anything up by hand.
	NearLimit bool
	// Weight biases how much volume this merchant takes.
	Weight int
	Bank   string
}

func MerchantSpecs() []MerchantSpec {
	return []MerchantSpec{
		// --- Kenya (7) -------------------------------------------------------
		{ID: "mch_001", Name: "Nakuru Fresh Produce", Country: platform.KE, PercentageBP: 150, FixedMinor: 1_000, FloatLimit: 5_000_000, HeadroomBP: 30000, Weight: 14, Bank: "Equity Bank Kenya"},
		{ID: "mch_002", Name: "Mombasa Water Utility", Country: platform.KE, PercentageBP: 90, FixedMinor: 500, FloatLimit: 12_000_000, HeadroomBP: 45000, Weight: 18, Bank: "KCB Bank Kenya"},
		{ID: "mch_003", Name: "Riverside Academy", Country: platform.KE, PercentageBP: 120, FixedMinor: 2_000, FloatLimit: 3_000_000, HeadroomBP: 25000, Weight: 6, Bank: "Co-operative Bank"},
		{ID: "mch_004", Name: "Thika Road Hardware", Country: platform.KE, PercentageBP: 175, FixedMinor: 1_500, FloatLimit: 2_500_000, HeadroomBP: 10200, Weight: 9, Bank: "Absa Bank Kenya", NearLimit: true},
		{ID: "mch_005", Name: "Kisumu Pharmacy Group", Country: platform.KE, PercentageBP: 135, FixedMinor: 1_000, FloatLimit: 4_000_000, HeadroomBP: 35000, Weight: 8, Bank: "NCBA Bank Kenya"},
		{ID: "mch_006", Name: "Eldoret Grain Millers", Country: platform.KE, PercentageBP: 100, FixedMinor: 2_500, FloatLimit: 9_000_000, HeadroomBP: 50000, Weight: 11, Bank: "Stanbic Bank Kenya"},
		{ID: "mch_007", Name: "Karen Veterinary Clinic", Country: platform.KE, PercentageBP: 200, FixedMinor: 500, FloatLimit: 1_500_000, HeadroomBP: 28000, Weight: 4, Bank: "Family Bank"},

		// --- Nigeria (5) -----------------------------------------------------
		{ID: "mch_008", Name: "Lekki Power Distribution", Country: platform.NG, PercentageBP: 110, FixedMinor: 5_000, FloatLimit: 40_000_000, HeadroomBP: 40000, Weight: 16, Bank: "Guaranty Trust Bank"},
		{ID: "mch_009", Name: "Ikeja Electronics Market", Country: platform.NG, PercentageBP: 185, FixedMinor: 2_500, FloatLimit: 15_000_000, HeadroomBP: 32000, Weight: 10, Bank: "Zenith Bank"},
		{ID: "mch_010", Name: "Abuja International School", Country: platform.NG, PercentageBP: 125, FixedMinor: 10_000, FloatLimit: 8_000_000, HeadroomBP: 26000, Weight: 5, Bank: "Access Bank"},
		{ID: "mch_011", Name: "Port Harcourt Logistics", Country: platform.NG, PercentageBP: 160, FixedMinor: 3_000, FloatLimit: 6_000_000, HeadroomBP: 10200, Weight: 7, Bank: "First Bank of Nigeria", NearLimit: true},
		{ID: "mch_012", Name: "Kano Textile Wholesalers", Country: platform.NG, PercentageBP: 145, FixedMinor: 2_000, FloatLimit: 20_000_000, HeadroomBP: 38000, Weight: 12, Bank: "United Bank for Africa"},
	}
}

// Normalised fills in the channel and currency derived from the country (C-1.8).
func Normalised() []MerchantSpec {
	out := MerchantSpecs()
	for i := range out {
		if out[i].Country == platform.KE {
			out[i].Channel = platform.ChannelMpesa
			out[i].Currency = platform.KES
		} else {
			out[i].Channel = platform.ChannelNIBSSTransfer
			out[i].Currency = platform.NGN
		}
	}
	return out
}

// pickMerchant chooses a merchant by weight, deterministically for a given rng.
func pickMerchant(rng *rand.Rand, specs []MerchantSpec, totalWeight int) *MerchantSpec {
	n := rng.Intn(totalWeight)
	for i := range specs {
		n -= specs[i].Weight
		if n < 0 {
			return &specs[i]
		}
	}
	return &specs[len(specs)-1]
}
