package platform

import "testing"

func TestDevTokenRoundTrip(t *testing.T) {
	want := Claims{Subject: "user_1", MerchantID: "mch_004", Role: RoleMerchant}
	got, err := ParseAuthorization("Bearer " + EncodeDevToken(want))
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseAuthorization_Rejects(t *testing.T) {
	cases := map[string]string{
		"absent":                      "",
		"not bearer":                  "Basic abc",
		"not a dev token":             "Bearer eyJhbGciOiJIUzI1NiJ9.x.y",
		"bad base64":                  "Bearer dev.!!!!",
		"merchant with no merchantId": "Bearer " + EncodeDevToken(Claims{Subject: "u", Role: RoleMerchant}),
		"unknown role":                "Bearer " + EncodeDevToken(Claims{Subject: "u", MerchantID: "m", Role: "admin"}),
		"no role":                     "Bearer " + EncodeDevToken(Claims{Subject: "u", MerchantID: "m"}),
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAuthorization(header); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

// C-6.3 / C-6.4: operations and finance must never be shown customer detail.
func TestRoleVisibility(t *testing.T) {
	for role, wantDetail := range map[Role]bool{
		RoleMerchant: true, RoleService: true, RoleOperations: false, RoleFinance: false,
	} {
		if got := role.CanSeeCustomerDetail(); got != wantDetail {
			t.Errorf("%s.CanSeeCustomerDetail() = %v, want %v", role, got, wantDetail)
		}
	}
	for role, wantCross := range map[Role]bool{
		RoleMerchant: false, RoleService: false, RoleOperations: true, RoleFinance: true,
	} {
		if got := role.IsCrossMerchant(); got != wantCross {
			t.Errorf("%s.IsCrossMerchant() = %v, want %v", role, got, wantCross)
		}
	}
}
