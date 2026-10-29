package config

import "testing"

func TestParseEnv(t *testing.T) {
	for _, env := range []string{"sandbox", "simulation", "production"} {
		got, err := ParseEnv(env)
		if err != nil {
			t.Fatalf("ParseEnv(%q) error: %v", env, err)
		}
		if string(got) != env {
			t.Errorf("ParseEnv(%q) = %q", env, got)
		}
	}
	if _, err := ParseEnv("invalid"); err == nil {
		t.Fatal("ParseEnv(\"invalid\") should fail")
	}
}

func TestEnvironmentBaseURL(t *testing.T) {
	tests := []struct {
		env  Environment
		want string
	}{
		{Sandbox, "https://gw-fatoora.zatca.gov.sa/e-invoicing/developer-portal"},
		{Simulation, "https://gw-fatoora.zatca.gov.sa/e-invoicing/simulation"},
		{Production, "https://gw-fatoora.zatca.gov.sa/e-invoicing/core"},
	}
	for _, tt := range tests {
		if got := tt.env.BaseURL(); got != tt.want {
			t.Errorf("%s.BaseURL() = %q, want %q", tt.env, got, tt.want)
		}
	}
}

func TestCSRConfigValidate(t *testing.T) {
	tp := validTaxpayer()
	csr := tp.CSR()
	if err := csr.Validate(); err != nil {
		t.Fatalf("valid CSR config rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*CSRConfig)
	}{
		{
			name: "missing required field",
			edit: func(c *CSRConfig) { c.OrgName = "   " },
		},
		{
			name: "invalid VAT digits",
			edit: func(c *CSRConfig) { c.OrgIdentifier = "39999999990000x" },
		},
		{
			name: "invalid VAT boundary",
			edit: func(c *CSRConfig) { c.OrgIdentifier = "199999999900003" },
		},
		{
			name: "invalid country",
			edit: func(c *CSRConfig) { c.Country = "S" },
		},
		{
			name: "invalid invoice map",
			edit: func(c *CSRConfig) { c.InvoiceType = "1102" },
		},
		{
			name: "invalid short address",
			edit: func(c *CSRConfig) { c.Location = "12345678" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := csr
			tt.edit(&got)
			if err := got.Validate(); err == nil {
				t.Fatal("CSRConfig.Validate() accepted malformed CSR data")
			}
		})
	}
}
