package sdk

import (
	"testing"

	"github.com/zatca-go/zatca/config"
)

func TestParseCSROutput(t *testing.T) {
	output := `===CSR===
-----BEGIN CERTIFICATE REQUEST-----
MIICFjCCAbwCAQAwdT...
-----END CERTIFICATE REQUEST-----
===KEY===
-----BEGIN EC PRIVATE KEY-----
MIGNAgEAMBAGByqGSM49...
-----END EC PRIVATE KEY-----`

	csr, key, err := parseCSROutput(output)
	if err != nil {
		t.Fatalf("parseCSROutput error: %v", err)
	}
	if csr == "" {
		t.Error("CSR should not be empty")
	}
	if key == "" {
		t.Error("Key should not be empty")
	}
	if csr[0:5] != "-----" {
		t.Errorf("CSR should start with PEM header, got: %q", csr[:20])
	}
}

func TestParseCSROutputMissing(t *testing.T) {
	_, _, err := parseCSROutput("no markers here")
	if err == nil {
		t.Error("expected error when markers missing")
	}
}

func TestEnvFlag(t *testing.T) {
	tests := []struct {
		env  string
		want string
	}{
		{"sandbox", "-nonprod"},
		{"simulation", "-sim"},
		{"production", ""},
	}
	for _, tt := range tests {
		s := &SDK{env: config.Environment(tt.env)}
		if got := s.envFlag(); got != tt.want {
			t.Errorf("envFlag(%s) = %q, want %q", tt.env, got, tt.want)
		}
	}
}
