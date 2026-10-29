package processor

import "testing"

func TestOnboardTaxpayerPreflightRejectsMalformedVATAndLocation(t *testing.T) {
	tests := []struct {
		name string
		vat  string
		loc  string
	}{
		{
			name: "vat has invalid boundary digit",
			vat:  "199999999900003",
			loc:  "RRRD2929",
		},
		{
			name: "vat has a non-digit",
			vat:  "39999999990000x",
			loc:  "RRRD2929",
		},
		{
			name: "location is not a short address",
			vat:  "310175397400003",
			loc:  "12345678",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			branch := &onboardBranchData{
				CompanyName:   "Test Company LTD",
				CompanyNameAR: "شركة اختبار المحدودة",
				VATReg:        tt.vat,
				CRN:           "1010010000",
				BranchName:    "Main Branch",
				Street:        "King Fahd Road",
				Building:      "1234",
				District:      "Al-Olaya",
				City:          "Riyadh",
				PostalCode:    "12345",
				Country:       "SA",
				BusinessCat:   "Supply activities",
				CSRLocation:   tt.loc,
			}

			if err := branch.taxpayer().Validate(); err == nil {
				t.Fatal("onboarding preflight accepted malformed taxpayer data")
			}
		})
	}
}
