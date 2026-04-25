package config

import (
	"strings"
	"testing"
)

func TestTaxpayerValidate(t *testing.T) {
	tp := &Taxpayer{}
	if err := tp.Validate(); err == nil {
		t.Error("expected validation error for empty taxpayer")
	}

	tp = validTaxpayer()
	if err := tp.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Bad VAT length
	tp.VATNumber = "123"
	if err := tp.Validate(); err == nil {
		t.Error("expected error for short VAT")
	}

	// Missing BusinessCategory
	tp = validTaxpayer()
	tp.BusinessCategory = ""
	if err := tp.Validate(); err == nil {
		t.Error("expected error for empty BusinessCategory")
	}
}

func TestTaxpayerCSR(t *testing.T) {
	tp := validTaxpayer()
	csr := tp.CSR()

	// CommonName format: TST-{9digits}-{VAT}
	if !strings.HasPrefix(csr.CommonName, "TST-") {
		t.Errorf("CommonName %q should start with TST-", csr.CommonName)
	}
	if !strings.HasSuffix(csr.CommonName, "-"+tp.VATNumber) {
		t.Errorf("CommonName %q should end with -%s", csr.CommonName, tp.VATNumber)
	}
	// TIN should be 9 digits
	parts := strings.Split(csr.CommonName, "-")
	if len(parts) < 3 {
		t.Fatalf("CommonName %q should have at least 3 parts", csr.CommonName)
	}
	if len(parts[1]) != 9 {
		t.Errorf("TIN part %q should be 9 digits", parts[1])
	}

	// SerialNumber format: 1-{Company}|2-TST|3-{UUID}
	if !strings.HasPrefix(csr.SerialNumber, "1-"+tp.CompanyName+"|2-TST|3-") {
		t.Errorf("SerialNumber %q has wrong format", csr.SerialNumber)
	}

	if csr.OrgIdentifier != tp.VATNumber {
		t.Errorf("OrgIdentifier = %q, want %q", csr.OrgIdentifier, tp.VATNumber)
	}
	if csr.OrgName != tp.CompanyName {
		t.Errorf("OrgName = %q, want %q", csr.OrgName, tp.CompanyName)
	}
	if csr.Country != "SA" {
		t.Errorf("Country = %q, want SA", csr.Country)
	}
	if csr.BusinessCategory != tp.BusinessCategory {
		t.Errorf("BusinessCategory = %q, want %q", csr.BusinessCategory, tp.BusinessCategory)
	}

	// Calling CSR() twice should produce same TIN (stable after first call)
	csr2 := tp.CSR()
	if csr.CommonName != csr2.CommonName {
		t.Errorf("CSR() not stable: %q vs %q", csr.CommonName, csr2.CommonName)
	}
}

func TestTaxpayerSeller(t *testing.T) {
	tp := validTaxpayer()
	seller := tp.Seller()

	if seller.Name != tp.CompanyName {
		t.Errorf("Name = %q, want %q", seller.Name, tp.CompanyName)
	}
	if seller.VAT != tp.VATNumber {
		t.Errorf("VAT = %q, want %q", seller.VAT, tp.VATNumber)
	}
	if seller.City != tp.City {
		t.Errorf("City = %q, want %q", seller.City, tp.City)
	}
}

func TestTaxpayerConfig(t *testing.T) {
	tp := validTaxpayer()
	cfg := tp.Config(Simulation, "999999")

	if cfg.Env != Simulation {
		t.Errorf("Env = %q, want simulation", cfg.Env)
	}
	if cfg.OTP != "999999" {
		t.Errorf("OTP = %q, want 999999", cfg.OTP)
	}
	if cfg.CSR.OrgName != tp.CompanyName {
		t.Errorf("CSR.OrgName = %q", cfg.CSR.OrgName)
	}
}

func validTaxpayer() *Taxpayer {
	return &Taxpayer{
		CompanyName:      "Test Company LTD",
		CompanyNameAR:    "شركة اختبار المحدودة",
		VATNumber:        "310175397400003",
		CRN:              "1010010000",
		BranchName:       "Main Branch",
		BusinessCategory: "Supply activities",
		Street:           "King Fahd Road",
		Building:         "1234",
		District:         "Al-Olaya",
		City:             "Riyadh",
		PostalCode:       "12345",
		LocationCode:     "RRRD2929",
	}
}
