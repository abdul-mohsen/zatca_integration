package config

import "fmt"

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
