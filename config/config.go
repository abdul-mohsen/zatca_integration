package config

import (
	"fmt"
	"strings"
)

// Environment represents a ZATCA API environment.
type Environment string

const (
	Sandbox    Environment = "sandbox"
	Simulation Environment = "simulation"
	Production Environment = "production"
)

// BaseURL returns the API base URL for the environment.
func (e Environment) BaseURL() string {
	switch e {
	case Sandbox:
		return "https://gw-fatoora.zatca.gov.sa/e-invoicing/developer-portal"
	case Simulation:
		return "https://gw-fatoora.zatca.gov.sa/e-invoicing/simulation"
	case Production:
		return "https://gw-fatoora.zatca.gov.sa/e-invoicing/core"
	default:
		return "https://gw-fatoora.zatca.gov.sa/e-invoicing/developer-portal"
	}
}

// CSRConfig holds the CSR generation configuration.
type CSRConfig struct {
	CommonName       string
	SerialNumber     string
	OrgIdentifier    string
	OrgUnit          string
	OrgName          string
	Country          string
	InvoiceType      string
	Location         string
	BusinessCategory string
}

// Validate checks the fields that are sent to the ZATCA CSR generator.
func (c CSRConfig) Validate() error {
	checks := []struct {
		value, name string
	}{
		{c.CommonName, "CommonName"},
		{c.SerialNumber, "SerialNumber"},
		{c.OrgIdentifier, "OrgIdentifier"},
		{c.OrgUnit, "OrgUnit"},
		{c.OrgName, "OrgName"},
		{c.Country, "Country"},
		{c.InvoiceType, "InvoiceType"},
		{c.Location, "Location"},
		{c.BusinessCategory, "BusinessCategory"},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.value) == "" {
			return fmt.Errorf("CSR.%s is required", check.name)
		}
	}

	if err := validateSaudiVAT(c.OrgIdentifier, "CSR.OrgIdentifier"); err != nil {
		return err
	}
	if err := validateCountryCode(c.Country); err != nil {
		return err
	}
	if err := validateInvoiceType(c.InvoiceType); err != nil {
		return err
	}
	if err := validateShortAddress(c.Location, "CSR.Location"); err != nil {
		return err
	}
	return nil
}

// SellerInfo holds seller/supplier information for invoices.
type SellerInfo struct {
	Name       string
	NameAR     string
	VAT        string
	CRN        string
	Street     string
	Building   string
	District   string
	City       string
	PostalCode string
	Country    string
}

// Config holds all configuration values.
type Config struct {
	Env                Environment
	OTP                string
	ComplianceUsername string
	CompliancePassword string
	ProductionUsername string
	ProductionPassword string
	XMLDir             string
	TenantID           string // tenant identifier (e.g. db_name) used to scope XML storage
	CSR                CSRConfig
	Seller             SellerInfo
}

// BaseURL returns the base URL for the configured environment.
func (c *Config) BaseURL() string {
	return c.Env.BaseURL()
}

// Validate checks the CSR configuration before an SDK or provider call.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is required")
	}
	return c.CSR.Validate()
}

// ParseEnv validates and returns an Environment from a string.
func ParseEnv(s string) (Environment, error) {
	e := Environment(s)
	switch e {
	case Sandbox, Simulation, Production:
		return e, nil
	default:
		return "", fmt.Errorf("invalid environment: %q (must be sandbox, simulation, or production)", s)
	}
}

func validateRequired(value, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("Taxpayer.%s is required", name)
	}
	return nil
}

func validateSaudiVAT(value, name string) error {
	if len(value) != 15 {
		return fmt.Errorf("%s must be a 15-digit Saudi VAT number starting and ending with 3, got %d characters", name, len(value))
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return fmt.Errorf("%s must contain only digits", name)
		}
	}
	if value[0] != '3' || value[14] != '3' {
		return fmt.Errorf("%s must start and end with digit 3", name)
	}
	return nil
}

func validateCountryCode(value string) error {
	if len(value) != 2 || value[0] < 'A' || value[0] > 'Z' || value[1] < 'A' || value[1] > 'Z' {
		return fmt.Errorf("CSR.Country must be a 2-letter uppercase ISO 3166-1 alpha-2 code")
	}
	return nil
}

func validateInvoiceType(value string) error {
	if len(value) != 4 {
		return fmt.Errorf("CSR.InvoiceType must be a four-digit functionality map using only 0 and 1")
	}
	for i := 0; i < len(value); i++ {
		if value[i] != '0' && value[i] != '1' {
			return fmt.Errorf("CSR.InvoiceType must be a four-digit functionality map using only 0 and 1")
		}
	}
	return nil
}

func validateShortAddress(value, name string) error {
	if len(value) != 8 {
		return fmt.Errorf("%s must contain four letters followed by four digits", name)
	}
	for i := 0; i < 4; i++ {
		if !isASCIILetter(value[i]) {
			return fmt.Errorf("%s must contain four letters followed by four digits", name)
		}
	}
	for i := 4; i < 8; i++ {
		if value[i] < '0' || value[i] > '9' {
			return fmt.Errorf("%s must contain four letters followed by four digits", name)
		}
	}
	return nil
}

func isASCIILetter(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z')
}
