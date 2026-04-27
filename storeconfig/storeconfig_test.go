package storeconfig

import (
	"encoding/base64"
	"testing"
)

// TestCertificatePEM_DecodesBinarySecurityToken verifies that CertificatePEM
// base64-decodes the stored binarySecurityToken once. ZATCA returns the token
// as Base64(<cert.pem body>) where the inner body is itself raw Base64(DER).
// fatoora's InvoiceSigningService base64-decodes the file once before
// CertificateFactory.generateCertificate, so cert.pem must contain the inner
// Base64(DER), not the raw doubly-encoded token.
func TestCertificatePEM_DecodesBinarySecurityToken(t *testing.T) {
	// Inner cert body — single line of base64(DER), as in the SDK's shipped
	// Data/Certificates/cert.pem.
	innerBody := "MIID3jCCA4SgAwIBAgITEQAAOAPF90Ajs/xcXwABAAA4Aw=="
	// What ZATCA's API returns:
	token := base64.StdEncoding.EncodeToString([]byte(innerBody))

	bz := &BranchZATCA{Certificate: token}
	got := bz.CertificatePEM()
	if got != innerBody {
		t.Errorf("CertificatePEM() = %q, want %q (one base64 decode of token)", got, innerBody)
	}
}

func TestCertificatePEM_Empty(t *testing.T) {
	bz := &BranchZATCA{Certificate: ""}
	if got := bz.CertificatePEM(); got != "" {
		t.Errorf("CertificatePEM() = %q, want empty", got)
	}
}

// Legacy data: if Certificate is already the decoded body (not valid base64),
// CertificatePEM should fall back to returning it unchanged so we don't break
// previously-onboarded branches.
func TestCertificatePEM_FallbackOnInvalidBase64(t *testing.T) {
	notBase64 := "@@@not_valid_base64@@@"
	bz := &BranchZATCA{Certificate: notBase64}
	if got := bz.CertificatePEM(); got != notBase64 {
		t.Errorf("CertificatePEM() = %q, want %q (fallback unchanged)", got, notBase64)
	}
}
