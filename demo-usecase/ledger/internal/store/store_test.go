package store

import (
	"strings"
	"testing"

	"github.com/mopay/ledger/internal/platform"
)

func entry(merchant, dir string, amt platform.Minor, cur platform.Currency) Entry {
	return Entry{MerchantID: merchant, Direction: dir, AmountMinor: amt, Currency: cur}
}

func group(entries ...Entry) EntryGroup {
	return EntryGroup{
		TransactionGroupID: "tg_99a2",
		EventType:          EventCollection,
		EventID:            "col_1",
		Entries:            entries,
	}
}

// C-3.5 / hard constraint 2: entries sharing a transactionGroupId must sum to zero.
func TestValidateGroup_BalancedIsAccepted(t *testing.T) {
	g := group(
		entry("mch_004", DirectionCredit, 250_00, platform.KES),
		entry("mopay_house", DirectionDebit, 250_00, platform.KES),
	)
	if err := ValidateGroup(g); err != nil {
		t.Fatalf("balanced group rejected: %v", err)
	}
}

func TestValidateGroup_UnbalancedIsRejectedWithGroupIDAndSum(t *testing.T) {
	g := group(
		entry("mch_004", DirectionCredit, 250_00, platform.KES),
		entry("mopay_house", DirectionDebit, 200_00, platform.KES),
	)
	err := ValidateGroup(g)
	if err == nil {
		t.Fatal("unbalanced group was accepted; C-3.5 requires rejection")
	}

	apiErr, ok := err.(*platform.APIError)
	if !ok {
		t.Fatalf("want *platform.APIError, got %T", err)
	}
	if apiErr.Code != platform.CodeLedgerNotBalanced {
		t.Errorf("code = %q, want %q", apiErr.Code, platform.CodeLedgerNotBalanced)
	}

	// The constraint is specific: the error carries the group id and the ACTUAL sum,
	// so whoever reads it can act without re-deriving the imbalance.
	if !strings.Contains(apiErr.Message, "tg_99a2") {
		t.Errorf("message must name the group id, got %q", apiErr.Message)
	}
	if !strings.Contains(apiErr.Message, "50.00") {
		t.Errorf("message must carry the actual sum (50.00), got %q", apiErr.Message)
	}
	if apiErr.Details["sumMinor"] != int64(5000) {
		t.Errorf("details.sumMinor = %v, want 5000", apiErr.Details["sumMinor"])
	}
	if apiErr.Details["transactionGroupId"] != "tg_99a2" {
		t.Errorf("details.transactionGroupId = %v", apiErr.Details["transactionGroupId"])
	}
}

func TestValidateGroup_MixedCurrenciesRejected(t *testing.T) {
	// Netting KES against NGN would "balance" numerically and be a residency bug.
	g := group(
		entry("mch_004", DirectionCredit, 250_00, platform.KES),
		entry("mopay_house", DirectionDebit, 250_00, platform.NGN),
	)
	if err := ValidateGroup(g); err == nil {
		t.Fatal("group mixing KES and NGN was accepted")
	}
}

func TestValidateGroup_RejectsMalformedEntries(t *testing.T) {
	cases := []struct {
		name string
		g    EntryGroup
	}{
		{"single entry cannot balance", group(entry("mch_004", DirectionCredit, 100, platform.KES))},
		{"negative amount", group(
			entry("mch_004", DirectionCredit, -100, platform.KES),
			entry("mopay_house", DirectionDebit, -100, platform.KES))},
		{"unknown direction", group(
			entry("mch_004", "sideways", 100, platform.KES),
			entry("mopay_house", DirectionDebit, 100, platform.KES))},
		{"missing merchant", group(
			entry("", DirectionCredit, 100, platform.KES),
			entry("mopay_house", DirectionDebit, 100, platform.KES))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateGroup(tc.g); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}
}

// C-3.4: every entry references the business event that produced it.
func TestValidateGroup_RequiresEventReference(t *testing.T) {
	g := group(
		entry("mch_004", DirectionCredit, 100, platform.KES),
		entry("mopay_house", DirectionDebit, 100, platform.KES),
	)
	g.EventID = ""
	if err := ValidateGroup(g); err == nil {
		t.Fatal("group without an eventId was accepted; C-3.4 requires the reference")
	}
}

// The sign convention that C-3.2's balance depends on.
func TestEntrySigned(t *testing.T) {
	if got := entry("m", DirectionCredit, 500, platform.KES).Signed(); got != 500 {
		t.Errorf("credit signed = %d, want 500", got)
	}
	if got := entry("m", DirectionDebit, 500, platform.KES).Signed(); got != -500 {
		t.Errorf("debit signed = %d, want -500", got)
	}
}
