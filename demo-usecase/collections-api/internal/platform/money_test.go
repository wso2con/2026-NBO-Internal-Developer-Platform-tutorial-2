package platform

import "testing"

// C-1.8: currency is derived from the channel, never accepted from a caller.
func TestCurrencyForChannel(t *testing.T) {
	cases := []struct {
		channel  Channel
		currency Currency
		country  Country
	}{
		{ChannelMpesa, KES, KE},
		{ChannelNIBSSTransfer, NGN, NG},
	}
	for _, tc := range cases {
		cur, ok := CurrencyForChannel(tc.channel)
		if !ok || cur != tc.currency {
			t.Errorf("CurrencyForChannel(%s) = %q,%v; want %q,true", tc.channel, cur, ok, tc.currency)
		}
		ctry, ok := CountryForChannel(tc.channel)
		if !ok || ctry != tc.country {
			t.Errorf("CountryForChannel(%s) = %q,%v; want %q,true", tc.channel, ctry, ok, tc.country)
		}
	}

	// SCOPE.md conflict 1 caps us at two channels. The others must not resolve.
	for _, c := range []Channel{"airtel_money", "paystack_card", "", "MPESA"} {
		if _, ok := CurrencyForChannel(c); ok {
			t.Errorf("channel %q resolved a currency but is not supported", c)
		}
	}
}

// C-2.8: percentage in basis points plus a fixed component, integer arithmetic throughout.
func TestFeeFor(t *testing.T) {
	cases := []struct {
		name   string
		gross  Minor
		bp     int
		fixed  Minor
		expect Minor
	}{
		{"1.5% of 10000.00 plus 10.00", 1_000_000, 150, 1_000, 16_000},
		{"zero percentage keeps the fixed part", 500_00, 0, 250, 250},
		{"rounds half up", 333, 150, 0, 5}, // 333 * 0.015 = 4.995 -> 5
		{"zero gross is just the fixed part", 0, 250, 100, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FeeFor(tc.gross, tc.bp, tc.fixed); got != tc.expect {
				t.Errorf("FeeFor(%d, %d, %d) = %d, want %d", tc.gross, tc.bp, tc.fixed, got, tc.expect)
			}
		})
	}
}

func TestMinorString(t *testing.T) {
	for _, tc := range []struct {
		in   Minor
		want string
	}{{250_00, "250.00"}, {5, "0.05"}, {-25_050, "-250.50"}, {0, "0.00"}} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("Minor(%d).String() = %q, want %q", tc.in, got, tc.want)
		}
	}
}
