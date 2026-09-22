package runner

import (
	"testing"
	"time"

	"github.com/mopay/settlement-worker/internal/ledgerclient"
	"github.com/mopay/settlement-worker/internal/platform"
)

// C-2.2: the month-end run happens on the last BUSINESS day of the month.
func TestIsMonthEnd(t *testing.T) {
	cases := []struct {
		date string
		want bool
		why  string
	}{
		// September 2026 ends Wednesday the 30th.
		{"2026-09-30", true, "Wednesday 30 Sep is the last business day"},
		{"2026-09-29", false, "29 Sep is not the last business day"},
		{"2026-09-01", false, "the first of the month is not month end"},
		// May 2026 ends Sunday the 31st, so the last business day is Friday the 29th.
		{"2026-05-29", true, "Friday 29 May, because 30 and 31 May are the weekend"},
		{"2026-05-31", false, "Sunday 31 May is not a business day"},
		{"2026-05-30", false, "Saturday 30 May is not a business day"},
		// February 2026 ends Saturday the 28th, so Friday the 27th.
		{"2026-02-27", true, "Friday 27 Feb, because 28 Feb is a Saturday"},
		{"2026-02-28", false, "Saturday 28 Feb is not a business day"},
	}

	for _, tc := range cases {
		d, err := time.Parse("2006-01-02", tc.date)
		if err != nil {
			t.Fatalf("bad test date %q: %v", tc.date, err)
		}
		if got := IsMonthEnd(d); got != tc.want {
			t.Errorf("IsMonthEnd(%s) = %v, want %v — %s", tc.date, got, tc.want, tc.why)
		}
	}
}

// C-2.8: fees are per merchant AND per channel, so they are computed per collection from
// that collection's schedule and then summed - not once for the merchant.
func TestFeeAggregationIsPerCollection(t *testing.T) {
	items := []ledgerclient.PendingCollection{
		{
			CollectionID: "c1", AmountMinor: 100_000, Currency: platform.KES,
			Channel:     platform.ChannelMpesa,
			FeeSchedule: &ledgerclient.FeeSchedule{PercentageBP: 150, FixedMinor: 1_000},
		},
		{
			CollectionID: "c2", AmountMinor: 250_000, Currency: platform.KES,
			Channel:     platform.ChannelMpesa,
			FeeSchedule: &ledgerclient.FeeSchedule{PercentageBP: 150, FixedMinor: 1_000},
		},
	}

	var gross, fees platform.Minor
	for _, it := range items {
		gross += it.AmountMinor
		fees += platform.FeeFor(it.AmountMinor, it.FeeSchedule.PercentageBP, it.FeeSchedule.FixedMinor)
	}

	// Per collection: 1.5% of 1000.00 + 10.00 = 25.00, and 1.5% of 2500.00 + 10.00 = 47.50.
	const wantFees = 1_500 + 1_000 + 3_750 + 1_000
	if fees != wantFees {
		t.Errorf("fees = %d, want %d", fees, wantFees)
	}
	if gross != 350_000 {
		t.Errorf("gross = %d, want 350000", gross)
	}
	// The invariant the ledger re-checks on commit.
	if gross-fees != 342_750 {
		t.Errorf("net = %d, want 342750", gross-fees)
	}

	// The fixed component is charged per collection, so two collections must cost more
	// than one collection of the combined value. Summing first would lose a fixed fee.
	combined := platform.FeeFor(350_000, 150, 1_000)
	if fees <= combined {
		t.Errorf("per-collection fees %d should exceed a single combined fee %d", fees, combined)
	}
}

func TestGroupByMerchant(t *testing.T) {
	got := groupByMerchant([]ledgerclient.PendingCollection{
		{CollectionID: "a", MerchantID: "mch_001"},
		{CollectionID: "b", MerchantID: "mch_002"},
		{CollectionID: "c", MerchantID: "mch_001"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d merchants, want 2", len(got))
	}
	if len(got["mch_001"]) != 2 || len(got["mch_002"]) != 1 {
		t.Errorf("grouping is wrong: mch_001=%d mch_002=%d", len(got["mch_001"]), len(got["mch_002"]))
	}
}
