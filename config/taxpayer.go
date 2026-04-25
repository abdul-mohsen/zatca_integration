package config

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/google/uuid"
)

// Taxpayer holds the bare minimum business info needed to configure
// CSR generation, seller details, and .env for any ZATCA environment.
//
// Required fields:
//   - CompanyName      (English legal name)
//   - CompanyNameAR    (Arabic legal name)
//   - VATNumber        (15-digit VAT registration number)
//   - CRN              (Commercial Registration Number)
//   - BranchName       (e.g. "Main Branch")
//   - BusinessCategory (activity type, e.g. "Supply activities", "Retail", …)
//   - Street, Building, District, City, PostalCode
//
// Auto-generated (do not set):
//   - TIN              — random 9-digit EGS serial embedded in CSR CommonName
//   - ComputerNumber   — UUID device identifier embedded in CSR SerialNumber
//
// Optional (have sensible defaults):
//   - InvoiceType      (defaults to "1100" = standard + simplified)
//   - LocationCode     (single short-address string, e.g. "RRRD2929"; default "0000000000")
//   - Country          (defaults to "SA")
type Taxpayer struct {
	// Required
	CompanyName      string // English legal name
	CompanyNameAR    string // Arabic legal name
	VATNumber        string // 15-digit VAT number (e.g. 310175397400003)
	CRN              string // Commercial Registration Number
	BranchName       string // Branch/unit name
	BusinessCategory string // Activity type (e.g. "Supply activities", "Retail")

	// Address (required)
	Street     string
	Building   string
	District   string
	City       string
	PostalCode string

	// Optional — defaults applied if empty
	InvoiceType  string // "1100" = standard+simplified (default)
	LocationCode string // Single short-address string (e.g. "RRRD2929", default "0000000000")
	Country      string // ISO 3166-1 alpha-2 (default: "SA")

	// Auto-generated — filled by applyDefaults(), do not set manually
	TIN            string // 9-digit EGS serial for CSR CommonName (e.g. "886431145")
	ComputerNumber string // UUID device identifier for CSR SerialNumber
}

// CSR returns a CSRConfig derived from the taxpayer data.
// CommonName format: TST-{TIN}-{VAT} (e.g. TST-886431145-310175397400003)
// SerialNumber format: 1-{CompanyName}|2-{UnitType}|3-{UUID}
func (t *Taxpayer) CSR() CSRConfig {
	t.applyDefaults()
	return CSRConfig{
		CommonName:       fmt.Sprintf("TST-%s-%s", t.TIN, t.VATNumber),
		SerialNumber:     fmt.Sprintf("1-%s|2-TST|3-%s", t.CompanyName, t.ComputerNumber),
		OrgIdentifier:    t.VATNumber,
		OrgUnit:          t.BranchName,
		OrgName:          t.CompanyName,
		Country:          t.Country,
		InvoiceType:      t.InvoiceType,
		Location:         t.LocationCode,
		BusinessCategory: t.BusinessCategory,
	}
}

// Seller returns a SellerInfo derived from the taxpayer data.
func (t *Taxpayer) Seller() SellerInfo {
	t.applyDefaults()
	return SellerInfo{
		Name:       t.CompanyName,
		NameAR:     t.CompanyNameAR,
		VAT:        t.VATNumber,
		CRN:        t.CRN,
		Street:     t.Street,
		Building:   t.Building,
		District:   t.District,
		City:       t.City,
		PostalCode: t.PostalCode,
		Country:    t.Country,
	}
}

// Config builds a full Config for the given environment and OTP.
func (t *Taxpayer) Config(env Environment, otp string) *Config {
	csr := t.CSR()
	seller := t.Seller()
	return &Config{
		Env:    env,
		OTP:    otp,
		CSR:    csr,
		Seller: seller,
	}
}

// Validate checks that all required fields are set.
func (t *Taxpayer) Validate() error {
	checks := []struct {
		val, name string
	}{
		{t.CompanyName, "CompanyName"},
		{t.CompanyNameAR, "CompanyNameAR"},
		{t.VATNumber, "VATNumber"},
		{t.CRN, "CRN"},
		{t.BranchName, "BranchName"},
		{t.BusinessCategory, "BusinessCategory"},
		{t.Street, "Street"},
		{t.Building, "Building"},
		{t.District, "District"},
		{t.City, "City"},
		{t.PostalCode, "PostalCode"},
		{t.LocationCode, "LocationCode"},
	}
	for _, c := range checks {
		if c.val == "" {
			return fmt.Errorf("Taxpayer.%s is required", c.name)
		}
	}
	if len(t.VATNumber) != 15 {
		return fmt.Errorf("VATNumber must be 15 digits, got %d", len(t.VATNumber))
	}
	return nil
}

func (t *Taxpayer) applyDefaults() {
	if t.Country == "" {
		t.Country = "SA"
	}
	if t.InvoiceType == "" {
		t.InvoiceType = "1100"
	}
	// Note: LocationCode has no safe default. ZATCA's KSA-12 rule requires
	// a 4-letter + 4-digit short address (e.g. "RRRD2929"). Validate()
	// rejects the empty value; do not silently substitute "0000000000".
	if t.TIN == "" {
		t.TIN = generateTIN()
	}
	if t.ComputerNumber == "" {
		t.ComputerNumber = uuid.New().String()
	}
}

// generateTIN returns a random 9-digit number string for the EGS serial.
func generateTIN() string {
	max := new(big.Int).SetInt64(999999999)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "100000000" // fallback
	}
	return fmt.Sprintf("%09d", n.Int64())
}
