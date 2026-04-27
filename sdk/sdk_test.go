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

func TestStripPEMArmor(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"already stripped", "MIID3jCCA4Sg", "MIID3jCCA4Sg"},
		{
			"cert with markers and newlines",
			"-----BEGIN CERTIFICATE-----\nMIID3jCC\nA4SgAwIB\n-----END CERTIFICATE-----",
			"MIID3jCCA4SgAwIB",
		},
		{
			"key with markers and CRLF",
			"-----BEGIN EC PRIVATE KEY-----\r\nMHcCAQE\r\nGByqGSM49\r\n-----END EC PRIVATE KEY-----\r\n",
			"MHcCAQEGByqGSM49",
		},
		{
			"trailing newline only",
			"MIID3jCCA4Sg\n",
			"MIID3jCCA4Sg",
		},
		{
			"internal spaces",
			"MIID 3jCC\tA4Sg",
			"MIID3jCCA4Sg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripPEMArmor(tt.in)
			if got != tt.want {
				t.Errorf("stripPEMArmor(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// Strict-base64 invariant: output must contain no whitespace or markers.
			for _, r := range got {
				if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
					t.Errorf("output contains whitespace: %q", got)
				}
			}
		})
	}
}

func TestGenerateQR_ExtractsEmbeddedQR(t *testing.T) {
	const qr = "AQ1Tb2xkRWRnZSBMTEMCDzMxMzEyMzA1MTQwMDAwMwMTMjAyNi0wNC0yN1QxMTowNzo1NA=="
	const xml = `<?xml version="1.0" encoding="UTF-8"?>
<Invoice>
    <cac:AdditionalDocumentReference>
        <cbc:ID>PIH</cbc:ID>
        <cac:Attachment>
            <cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain">prevhash==</cbc:EmbeddedDocumentBinaryObject>
        </cac:Attachment>
    </cac:AdditionalDocumentReference>
    <cac:AdditionalDocumentReference>
        <cbc:ID>QR</cbc:ID>
        <cac:Attachment>
            <cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain">` + qr + `</cbc:EmbeddedDocumentBinaryObject>
        </cac:Attachment>
    </cac:AdditionalDocumentReference>
</Invoice>`
	s := &SDK{}
	got, err := s.GenerateQR(xml)
	if err != nil {
		t.Fatalf("GenerateQR error: %v", err)
	}
	if got != qr {
		t.Errorf("GenerateQR = %q, want %q", got, qr)
	}
}

func TestGenerateQR_MissingQRReturnsError(t *testing.T) {
	const xml = `<?xml version="1.0"?><Invoice><cac:AdditionalDocumentReference><cbc:ID>PIH</cbc:ID><cac:Attachment><cbc:EmbeddedDocumentBinaryObject>x</cbc:EmbeddedDocumentBinaryObject></cac:Attachment></cac:AdditionalDocumentReference></Invoice>`
	s := &SDK{}
	if _, err := s.GenerateQR(xml); err == nil {
		t.Error("expected error when QR element absent, got nil")
	}
}

func TestGenerateQR_EmptyQRReturnsError(t *testing.T) {
	const xml = `<cac:AdditionalDocumentReference><cbc:ID>QR</cbc:ID><cac:Attachment><cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain"></cbc:EmbeddedDocumentBinaryObject></cac:Attachment></cac:AdditionalDocumentReference>`
	s := &SDK{}
	if _, err := s.GenerateQR(xml); err == nil {
		t.Error("expected error when QR EmbeddedDocumentBinaryObject body empty, got nil")
	}
}
