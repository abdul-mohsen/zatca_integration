package invoice

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/base64"
	"errors"
	"math/big"
	"testing"
	"time"
)

//go:embed testdata/*.xml
var importFixtures embed.FS

func TestParseUBLValidArabicAndUnknownData(t *testing.T) {
	data, err := importFixtures.ReadFile("testdata/valid.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	result, err := ParseSignedUBL(data, ImportOptions{})
	if err != nil {
		t.Fatalf("ParseSignedUBL() error: %v", err)
	}
	if result.Contract != UBLImportContract || result.Version != UBLImportVersion {
		t.Fatalf("contract = %q/%q", result.Contract, result.Version)
	}
	if result.Source.ContentHash == "" || result.Source.Size != int64(len(data)) {
		t.Fatalf("source evidence = %+v", result.Source)
	}
	if result.IdentityKey != "uuid:550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("identity key = %q", result.IdentityKey)
	}
	if result.Invoice.Supplier.Name != "Riyadh Supply Company" ||
		result.Invoice.Supplier.NameAR != "شركة توريد الرياض" {
		t.Fatalf("supplier names = %q/%q", result.Invoice.Supplier.Name, result.Invoice.Supplier.NameAR)
	}
	if len(result.Invoice.Lines) != 1 ||
		result.Invoice.Lines[0].Quantity != "2" ||
		result.Invoice.Lines[0].UnitPrice != "50.00" {
		t.Fatalf("line mapping = %+v", result.Invoice.Lines)
	}
	if result.Validation.SchemaStatus != SchemaStatusUnavailable {
		t.Fatalf("schema status = %q, want unavailable without an XSD resource", result.Validation.SchemaStatus)
	}
	if result.Validation.SignatureStatus != SignatureStatusUnsigned {
		t.Fatalf("signature status = %q, want unsigned", result.Validation.SignatureStatus)
	}
	if !result.Validation.ReviewRequired {
		t.Fatal("unsigned supplier data must remain review-gated")
	}
	if len(result.UnknownExtensions) != 1 {
		t.Fatalf("unknown extensions = %d, want 1", len(result.UnknownExtensions))
	}
	if len(result.UnsupportedFields) != 1 {
		t.Fatalf("unsupported fields = %d, want 1", len(result.UnsupportedFields))
	}
}

func TestParseUBLClassifiesSchemaInvalidWithoutDroppingEvidence(t *testing.T) {
	data, err := importFixtures.ReadFile("testdata/schema-invalid.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	result, err := ParseUBL(data, ImportOptions{})
	if err != nil {
		t.Fatalf("ParseUBL() error: %v", err)
	}
	if result.Validation.SchemaStatus != SchemaStatusInvalid {
		t.Fatalf("schema status = %q, want invalid", result.Validation.SchemaStatus)
	}
	if len(result.Validation.Issues) == 0 {
		t.Fatal("schema-invalid fixture produced no validation issues")
	}
	if result.Source.ContentHash == "" {
		t.Fatal("schema-invalid evidence lost source hash")
	}
	if result.IdentityKey != "" {
		t.Fatalf("incomplete invoice identity = %q, want empty", result.IdentityKey)
	}
}

func TestParseUBLRejectsMalformedOversizedAndUnsafeXML(t *testing.T) {
	malformed, err := importFixtures.ReadFile("testdata/malformed.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, err := ParseUBL(malformed, ImportOptions{}); !errors.Is(err, ErrUBLImportMalformed) {
		t.Fatalf("malformed error = %v, want ErrUBLImportMalformed", err)
	}
	valid, err := importFixtures.ReadFile("testdata/unsigned.xml")
	if err != nil {
		t.Fatalf("read valid fixture: %v", err)
	}
	if _, err := ParseUBL(append(valid, []byte("trailing")...), ImportOptions{}); !errors.Is(err, ErrUBLImportMalformed) {
		t.Fatalf("trailing data error = %v, want ErrUBLImportMalformed", err)
	}

	if _, err := ParseUBL(bytes.Repeat([]byte("x"), MaxUBLImportBytes+1), ImportOptions{}); !errors.Is(err, ErrUBLImportOversized) {
		t.Fatalf("oversized error = %v, want ErrUBLImportOversized", err)
	}

	unsafe := []byte(`<?xml version="1.0"?><!DOCTYPE Invoice [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><Invoice/>`)
	if _, err := ParseUBL(unsafe, ImportOptions{}); !errors.Is(err, ErrUBLImportUnsafe) {
		t.Fatalf("unsafe error = %v, want ErrUBLImportUnsafe", err)
	}
}

func TestParseUBLSignatureStateIsNotClaimedWithoutTrustRoots(t *testing.T) {
	data, err := importFixtures.ReadFile("testdata/unsigned.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	signed := bytes.Replace(data,
		[]byte("</Invoice>"),
		[]byte(`<ext:UBLExtensions xmlns:ext="urn:oasis:names:specification:ubl:schema:xsd:CommonExtensionComponents-2"><ext:UBLExtension><ext:ExtensionContent><ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#"><ds:SignedInfo><ds:Reference><ds:DigestValue>digest</ds:DigestValue></ds:Reference></ds:SignedInfo><ds:SignatureValue>signature</ds:SignatureValue><ds:KeyInfo><ds:X509Data><ds:X509Certificate>not-a-certificate</ds:X509Certificate></ds:X509Data></ds:KeyInfo></ds:Signature></ext:ExtensionContent></ext:UBLExtension></ext:UBLExtensions></Invoice>`),
		1,
	)
	result, err := ParseUBL(signed, ImportOptions{})
	if err != nil {
		t.Fatalf("ParseUBL() error: %v", err)
	}
	if result.Signature.Status != SignatureStatusInvalid {
		t.Fatalf("signature status = %q, want invalid for malformed certificate", result.Signature.Status)
	}
	if result.Validation.SignatureStatus == SignatureStatusVerified {
		t.Fatal("parser claimed verification without a valid certificate and trust roots")
	}
}

func TestParseUBLClassifiesStructurallySignedXMLAsUnverifiedWithoutRoots(t *testing.T) {
	data, err := importFixtures.ReadFile("testdata/unsigned.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "fixture-signer"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	signed := bytes.Replace(data,
		[]byte("</Invoice>"),
		[]byte(`<ext:UBLExtensions xmlns:ext="urn:oasis:names:specification:ubl:schema:xsd:CommonExtensionComponents-2"><ext:UBLExtension><ext:ExtensionContent><ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#"><ds:SignedInfo><ds:SignatureMethod Algorithm="ecdsa-sha256"/><ds:Reference><ds:DigestValue>digest</ds:DigestValue></ds:Reference></ds:SignedInfo><ds:SignatureValue>signature</ds:SignatureValue><ds:KeyInfo><ds:X509Data><ds:X509Certificate>`+base64.StdEncoding.EncodeToString(certificate)+`</ds:X509Certificate></ds:X509Data></ds:KeyInfo></ds:Signature></ext:ExtensionContent></ext:UBLExtension></ext:UBLExtensions></Invoice>`),
		1,
	)
	result, err := ParseUBL(signed, ImportOptions{})
	if err != nil {
		t.Fatalf("ParseUBL() error: %v", err)
	}
	if result.Signature.Status != SignatureStatusUnverified {
		t.Fatalf("signature status = %q, want unverified", result.Signature.Status)
	}
	if result.Signature.CertificateSHA256 == "" || result.Signature.CertificateSubject == "" {
		t.Fatalf("certificate evidence = %+v", result.Signature)
	}
}

func TestParseUBLSchemaValidatorAndDuplicateIdentityAreDeterministic(t *testing.T) {
	data, err := importFixtures.ReadFile("testdata/unsigned.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	duplicateData, err := importFixtures.ReadFile("testdata/duplicate.xml")
	if err != nil {
		t.Fatalf("read duplicate fixture: %v", err)
	}
	validator := func([]byte) []ValidationIssue {
		return []ValidationIssue{{Code: "fixture_schema_error", Message: "fixture schema rejected", Severity: "error"}}
	}
	first, err := ParseUBL(data, ImportOptions{SchemaValidator: validator})
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	second, err := ParseUBL(data, ImportOptions{SchemaValidator: validator})
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if first.IdentityKey != second.IdentityKey || first.Source.ContentHash != second.Source.ContentHash {
		t.Fatalf("identity/hash not deterministic: %q/%q vs %q/%q", first.IdentityKey, first.Source.ContentHash, second.IdentityKey, second.Source.ContentHash)
	}
	duplicate, err := ParseUBL(duplicateData, ImportOptions{})
	if err != nil {
		t.Fatalf("duplicate parse: %v", err)
	}
	if duplicate.IdentityKey != first.IdentityKey {
		t.Fatalf("duplicate identity = %q, want %q", duplicate.IdentityKey, first.IdentityKey)
	}
	if duplicate.Source.ContentHash == first.Source.ContentHash {
		t.Fatal("duplicate fixture should have distinct content hash for identity-only duplicate detection")
	}
	if first.Validation.SchemaStatus != SchemaStatusInvalid {
		t.Fatalf("schema validator result = %q, want invalid", first.Validation.SchemaStatus)
	}
}

func TestParseUBLSchemaWarningsDoNotClaimSchemaInvalid(t *testing.T) {
	data, err := importFixtures.ReadFile("testdata/unsigned.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	result, err := ParseUBL(data, ImportOptions{
		SchemaValidator: func([]byte) []ValidationIssue {
			return []ValidationIssue{{Code: "fixture_schema_warning", Message: "non-blocking schema warning", Severity: "warning"}}
		},
	})
	if err != nil {
		t.Fatalf("ParseUBL() error: %v", err)
	}
	if result.Validation.SchemaStatus != SchemaStatusValid {
		t.Fatalf("schema status = %q, want valid for warning-only schema result", result.Validation.SchemaStatus)
	}
	if len(result.Validation.Issues) != 1 || result.Validation.Issues[0].Severity != "warning" {
		t.Fatalf("schema warning evidence = %+v", result.Validation.Issues)
	}
}
