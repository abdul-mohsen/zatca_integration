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
