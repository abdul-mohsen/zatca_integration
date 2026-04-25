package zatca

import (
	"testing"

	"github.com/zatca-go/zatca/config"
)

func TestPemToBase64(t *testing.T) {
	pem := `-----BEGIN CERTIFICATE REQUEST-----
MIIBkTCB+wIBADBTMQswCQYDVQQGEwJTQTENMAsGA1UECwwEVGVzdDEVMBMGA1UE
CgwMVGVzdCBPcmcgTFREMR4wHAYDVQQDDBVUU1QtMTIzNDU2Nzg5LTMwMDAwMDAw
QUFHZ0F3SUJBZ0lCQURBTkJna3Foa2lHOXcwQkFRc0ZBREE9TVFzd0NRWURWUVFE
-----END CERTIFICATE REQUEST-----`

	b64 := pemToBase64(pem)
	if b64 == "" {
		t.Fatal("empty result")
	}
	if len(b64) < 10 {
		t.Error("result too short")
	}
	// Should not contain PEM markers
	if contains(b64, "-----") {
		t.Error("should not contain PEM markers")
	}
	// Should not contain newlines
	if contains(b64, "\n") {
		t.Error("should not contain newlines")
	}
}

func TestNew(t *testing.T) {
	// Test creating service from config
	cfg := &config.Config{
		Env: config.Sandbox,
		OTP: "123456",
	}
	svc := New(cfg)
	if svc == nil {
		t.Fatal("service is nil")
	}
	if svc.Client == nil {
		t.Fatal("client is nil")
	}
	if svc.SDK == nil {
		t.Fatal("SDK is nil")
	}
}

func contains(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
