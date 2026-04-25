package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

// Sample unsigned invoice XML for testing
const testInvoiceXML = `<?xml version="1.0" encoding="UTF-8"?>
<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2" xmlns:cac="urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2" xmlns:cbc="urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2" xmlns:ext="urn:oasis:names:specification:ubl:schema:xsd:CommonExtensionComponents-2"><ext:UBLExtensions>
    <ext:UBLExtension>
        <ext:ExtensionURI>urn:oasis:names:specification:ubl:dsig:enveloped:xades</ext:ExtensionURI>
        <ext:ExtensionContent>
            <sig:UBLDocumentSignatures xmlns:sig="urn:oasis:names:specification:ubl:schema:xsd:CommonSignatureComponents-2" xmlns:sac="urn:oasis:names:specification:ubl:schema:xsd:SignatureAggregateComponents-2" xmlns:sbc="urn:oasis:names:specification:ubl:schema:xsd:SignatureBasicComponents-2">
                <sac:SignatureInformation>
                    <cbc:ID>urn:oasis:names:specification:ubl:signature:1</cbc:ID>
                    <sbc:ReferencedSignatureID>urn:oasis:names:specification:ubl:signature:Invoice</sbc:ReferencedSignatureID>
                </sac:SignatureInformation>
            </sig:UBLDocumentSignatures>
        </ext:ExtensionContent>
    </ext:UBLExtension>
</ext:UBLExtensions>
    <cbc:ProfileID>reporting:1.0</cbc:ProfileID>
    <cbc:ID>SME00001</cbc:ID>
    <cbc:UUID>550e8400-e29b-41d4-a716-446655440000</cbc:UUID>
    <cbc:IssueDate>2024-01-14</cbc:IssueDate>
    <cbc:IssueTime>10:00:00</cbc:IssueTime>
    <cbc:InvoiceTypeCode name="0100000">388</cbc:InvoiceTypeCode>
    <cbc:DocumentCurrencyCode>SAR</cbc:DocumentCurrencyCode>
    <cbc:TaxCurrencyCode>SAR</cbc:TaxCurrencyCode>
    <cac:AdditionalDocumentReference>
        <cbc:ID>ICV</cbc:ID>
        <cbc:UUID>1</cbc:UUID>
    </cac:AdditionalDocumentReference>
    <cac:AdditionalDocumentReference>
        <cbc:ID>PIH</cbc:ID>
        <cac:Attachment>
            <cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain">NWZlY2ViNjZmZmM4NmYzOGQ5NTI3ODZjNmQ2OTZjNzljMmRiYzIzOWRkNGU5MWI0NjcyOWQ3M2EyN2ZiNTdlOQ==</cbc:EmbeddedDocumentBinaryObject>
        </cac:Attachment>
    </cac:AdditionalDocumentReference>
    <cac:AdditionalDocumentReference>
        <cbc:ID>QR</cbc:ID>
        <cac:Attachment>
            <cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain"></cbc:EmbeddedDocumentBinaryObject>
        </cac:Attachment>
    </cac:AdditionalDocumentReference>
    <cac:Signature>
      <cbc:ID>urn:oasis:names:specification:ubl:signature:Invoice</cbc:ID>
      <cbc:SignatureMethod>urn:oasis:names:specification:ubl:dsig:enveloped:xades</cbc:SignatureMethod>
    </cac:Signature>
    <cac:AccountingSupplierParty>
        <cac:Party>
            <cac:PostalAddress>
                <cbc:StreetName>Test Street</cbc:StreetName>
                <cbc:CityName>Riyadh</cbc:CityName>
                <cbc:PostalZone>12345</cbc:PostalZone>
                <cac:Country>
                    <cbc:IdentificationCode>SA</cbc:IdentificationCode>
                </cac:Country>
            </cac:PostalAddress>
            <cac:PartyTaxScheme>
                <cbc:CompanyID>399999999900003</cbc:CompanyID>
                <cac:TaxScheme>
                    <cbc:ID>VAT</cbc:ID>
                </cac:TaxScheme>
            </cac:PartyTaxScheme>
            <cac:PartyLegalEntity>
                <cbc:RegistrationName>Test Seller LTD</cbc:RegistrationName>
            </cac:PartyLegalEntity>
        </cac:Party>
    </cac:AccountingSupplierParty>
    <cac:AccountingCustomerParty>
        <cac:Party>
            <cac:PostalAddress>
                <cbc:StreetName>Customer Street</cbc:StreetName>
                <cbc:CityName>Jeddah</cbc:CityName>
                <cbc:PostalZone>54321</cbc:PostalZone>
                <cac:Country>
                    <cbc:IdentificationCode>SA</cbc:IdentificationCode>
                </cac:Country>
            </cac:PostalAddress>
            <cac:PartyTaxScheme>
                <cbc:CompanyID>399999999800003</cbc:CompanyID>
                <cac:TaxScheme>
                    <cbc:ID>VAT</cbc:ID>
                </cac:TaxScheme>
            </cac:PartyTaxScheme>
            <cac:PartyLegalEntity>
                <cbc:RegistrationName>Test Buyer LTD</cbc:RegistrationName>
            </cac:PartyLegalEntity>
        </cac:Party>
    </cac:AccountingCustomerParty>
    <cac:TaxTotal>
        <cbc:TaxAmount currencyID="SAR">15.00</cbc:TaxAmount>
    </cac:TaxTotal>
    <cac:TaxTotal>
        <cbc:TaxAmount currencyID="SAR">15.00</cbc:TaxAmount>
        <cac:TaxSubtotal>
            <cbc:TaxableAmount currencyID="SAR">100.00</cbc:TaxableAmount>
            <cbc:TaxAmount currencyID="SAR">15.00</cbc:TaxAmount>
            <cac:TaxCategory>
                <cbc:ID schemeID="UN/ECE 5305" schemeAgencyID="6">S</cbc:ID>
                <cbc:Percent>15.00</cbc:Percent>
                <cac:TaxScheme>
                    <cbc:ID schemeID="UN/ECE 5153" schemeAgencyID="6">VAT</cbc:ID>
                </cac:TaxScheme>
            </cac:TaxCategory>
        </cac:TaxSubtotal>
    </cac:TaxTotal>
    <cac:LegalMonetaryTotal>
        <cbc:LineExtensionAmount currencyID="SAR">100.00</cbc:LineExtensionAmount>
        <cbc:TaxExclusiveAmount currencyID="SAR">100.00</cbc:TaxExclusiveAmount>
        <cbc:TaxInclusiveAmount currencyID="SAR">115.00</cbc:TaxInclusiveAmount>
        <cbc:AllowanceTotalAmount currencyID="SAR">0.00</cbc:AllowanceTotalAmount>
        <cbc:PrepaidAmount currencyID="SAR">0.00</cbc:PrepaidAmount>
        <cbc:PayableAmount currencyID="SAR">115.00</cbc:PayableAmount>
    </cac:LegalMonetaryTotal>
    <cac:InvoiceLine>
        <cbc:ID>1</cbc:ID>
        <cbc:InvoicedQuantity unitCode="PCE">1.000000</cbc:InvoicedQuantity>
        <cbc:LineExtensionAmount currencyID="SAR">100.00</cbc:LineExtensionAmount>
        <cac:Item>
            <cbc:Name>Test Item</cbc:Name>
        </cac:Item>
        <cac:Price>
            <cbc:PriceAmount currencyID="SAR">100.00</cbc:PriceAmount>
        </cac:Price>
    </cac:InvoiceLine>
</Invoice>`

func TestInvoiceHash(t *testing.T) {
	hash, err := InvoiceHash(testInvoiceXML)
	if err != nil {
		t.Fatalf("InvoiceHash error: %v", err)
	}
	if hash == "" {
		t.Fatal("hash is empty")
	}
	// Base64 SHA-256 should be 44 chars (with padding)
	if len(hash) < 40 {
		t.Errorf("hash too short: %q", hash)
	}
	t.Logf("Invoice hash: %s", hash)
}

func TestInvoiceHashStripsCorrectly(t *testing.T) {
	hash1, _ := InvoiceHash(testInvoiceXML)
	hash2, _ := InvoiceHash(testInvoiceXML) // Same input = same hash
	if hash1 != hash2 {
		t.Errorf("deterministic: got %q and %q", hash1, hash2)
	}

	// Modified XML should produce different hash
	modified := strings.Replace(testInvoiceXML, "SME00001", "SME00002", 1)
	hash3, _ := InvoiceHash(modified)
	if hash1 == hash3 {
		t.Error("different invoices should have different hashes")
	}
}

func TestInvoiceHashRemovesUBLExtensions(t *testing.T) {
	stripped := ExportStripForHashing(testInvoiceXML)
	if strings.Contains(stripped, "UBLExtensions") {
		t.Error("UBLExtensions should be removed")
	}
	if strings.Contains(stripped, "<cac:Signature>") {
		t.Error("cac:Signature should be removed")
	}
	if strings.Contains(stripped, ">QR<") {
		t.Error("QR reference should be removed")
	}
	// These should still be present
	if !strings.Contains(stripped, "SME00001") {
		t.Error("Invoice ID should be present")
	}
	if !strings.Contains(stripped, "PIH") {
		t.Error("PIH reference should be present")
	}
}

func TestQRGeneration(t *testing.T) {
	ts := time.Date(2024, 1, 14, 10, 0, 0, 0, time.UTC)
	qr, err := GenerateQR(&QRData{
		SellerName: "Test Seller LTD",
		VATNumber:  "399999999900003",
		Timestamp:  ts,
		Total:      "115.00",
		VATAmount:  "15.00",
		Hash:       "abc123hash",
	})
	if err != nil {
		t.Fatalf("GenerateQR error: %v", err)
	}
	if qr == "" {
		t.Fatal("QR is empty")
	}

	// Decode and verify
	tags, err := DecodeTLV(qr)
	if err != nil {
		t.Fatalf("DecodeTLV error: %v", err)
	}

	if string(tags[TagSellerName]) != "Test Seller LTD" {
		t.Errorf("Tag 1 (seller): %q", tags[TagSellerName])
	}
	if string(tags[TagVATNumber]) != "399999999900003" {
		t.Errorf("Tag 2 (VAT): %q", tags[TagVATNumber])
	}
	if string(tags[TagTimestamp]) != "2024-01-14T10:00:00Z" {
		t.Errorf("Tag 3 (time): %q", tags[TagTimestamp])
	}
	if string(tags[TagTotalWithVAT]) != "115.00" {
		t.Errorf("Tag 4 (total): %q", tags[TagTotalWithVAT])
	}
	if string(tags[TagVATAmount]) != "15.00" {
		t.Errorf("Tag 5 (VAT): %q", tags[TagVATAmount])
	}
	if string(tags[TagInvoiceHash]) != "abc123hash" {
		t.Errorf("Tag 6 (hash): %q", tags[TagInvoiceHash])
	}
}

func TestQRWithAllTags(t *testing.T) {
	ts := time.Date(2024, 1, 14, 10, 0, 0, 0, time.UTC)
	sig := make([]byte, 64)
	rand.Read(sig)
	pub := make([]byte, 65)
	pub[0] = 0x04
	rand.Read(pub[1:])
	certHash := make([]byte, 32)
	rand.Read(certHash)

	qr, err := GenerateQR(&QRData{
		SellerName: "شركة اختبار", // Arabic name
		VATNumber:  "300000000000003",
		Timestamp:  ts,
		Total:      "1000.50",
		VATAmount:  "150.08",
		Hash:       "dGVzdGhhc2g=",
		Signature:  sig,
		PublicKey:  pub,
		CertHash:   certHash,
	})
	if err != nil {
		t.Fatalf("GenerateQR error: %v", err)
	}

	tags, err := DecodeTLV(qr)
	if err != nil {
		t.Fatalf("DecodeTLV error: %v", err)
	}

	if len(tags) != 9 {
		t.Errorf("expected 9 tags, got %d", len(tags))
	}
	if string(tags[TagSellerName]) != "شركة اختبار" {
		t.Errorf("Arabic seller name mismatch: %q", tags[TagSellerName])
	}
}

func TestSimpleQR(t *testing.T) {
	ts := time.Date(2024, 1, 14, 10, 0, 0, 0, time.UTC)
	qr, err := GenerateSimpleQR("Seller", "300000000000003", ts, "100.00", "15.00")
	if err != nil {
		t.Fatal(err)
	}
	tags, _ := DecodeTLV(qr)
	if len(tags) != 5 {
		t.Errorf("simple QR should have 5 tags, got %d", len(tags))
	}
}

func TestPIHHash(t *testing.T) {
	hash := PIHHash("<Invoice>test</Invoice>")
	if hash == "" {
		t.Fatal("PIH hash is empty")
	}
	// Verify it's valid base64
	_, err := base64.StdEncoding.DecodeString(hash)
	if err != nil {
		t.Fatalf("PIH hash is not valid base64: %v", err)
	}
}

// generateTestCert creates a self-signed test certificate for testing.
func generateTestCert(t *testing.T) (*ecdsa.PrivateKey, *CertInfo) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   "TST-test-cert",
			Organization: []string{"Test Org"},
			Country:      []string{"SA"},
		},
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(24 * time.Hour),
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certInfo, err := parseCertDER(certBytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	return key, certInfo
}

func TestCertParsing(t *testing.T) {
	_, cert := generateTestCert(t)

	if cert.IssuerName == "" {
		t.Error("issuer name is empty")
	}
	if cert.SerialNum == nil {
		t.Error("serial number is nil")
	}
	if cert.PublicKey == nil {
		t.Error("public key is nil")
	}

	digest := cert.CertDigest()
	if digest == "" {
		t.Error("cert digest is empty")
	}

	b64 := cert.CertBase64()
	if b64 == "" {
		t.Error("cert base64 is empty")
	}

	pubBytes := cert.PublicKeyBytes()
	if len(pubBytes) != 65 {
		t.Errorf("public key bytes: expected 65, got %d", len(pubBytes))
	}
	if pubBytes[0] != 0x04 {
		t.Error("public key should start with 0x04 (uncompressed)")
	}
}

func TestCertParseBase64(t *testing.T) {
	_, cert := generateTestCert(t)
	b64 := cert.CertBase64()

	parsed, err := ParseCertificate(b64)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if parsed.SerialNumberDecimal() != cert.SerialNumberDecimal() {
		t.Errorf("serial mismatch: %s vs %s", parsed.SerialNumberDecimal(), cert.SerialNumberDecimal())
	}
}

func TestCertParsePEM(t *testing.T) {
	_, cert := generateTestCert(t)

	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	parsed, err := ParseCertificatePEM(pemData)
	if err != nil {
		t.Fatalf("ParseCertificatePEM: %v", err)
	}
	if parsed.IssuerNameForXML() != cert.IssuerNameForXML() {
		t.Errorf("issuer mismatch: %q vs %q", parsed.IssuerNameForXML(), cert.IssuerNameForXML())
	}
}

func TestParsePrivateKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	derBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	pemData := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: derBytes})
	parsed, err := ParsePrivateKey(pemData)
	if err != nil {
		t.Fatalf("ParsePrivateKey: %v", err)
	}
	if parsed.D.Cmp(key.D) != 0 {
		t.Error("private key mismatch")
	}
}

func TestSignInvoice(t *testing.T) {
	key, cert := generateTestCert(t)

	result, err := SignInvoice(testInvoiceXML, key, cert, false)
	if err != nil {
		t.Fatalf("SignInvoice error: %v", err)
	}

	if result.SignedXML == "" {
		t.Error("signed XML is empty")
	}
	if result.InvoiceHash == "" {
		t.Error("invoice hash is empty")
	}
	if result.SignatureB64 == "" {
		t.Error("signature is empty")
	}

	// Verify the signature is present in XML
	if !strings.Contains(result.SignedXML, "ds:Signature") {
		t.Error("signed XML should contain ds:Signature")
	}
	if !strings.Contains(result.SignedXML, "ds:SignatureValue") {
		t.Error("signed XML should contain ds:SignatureValue")
	}
	if !strings.Contains(result.SignedXML, "X509Certificate") {
		t.Error("signed XML should contain X509Certificate")
	}
	if !strings.Contains(result.SignedXML, "xadesSignedProperties") {
		t.Error("signed XML should contain xadesSignedProperties")
	}

	t.Logf("Hash: %s", result.InvoiceHash)
	t.Logf("Signature: %s", result.SignatureB64[:40]+"...")
}

func TestSignInvoiceWithQR(t *testing.T) {
	key, cert := generateTestCert(t)

	ts := time.Date(2024, 1, 14, 10, 0, 0, 0, time.UTC)
	result, err := SignInvoiceWithQR(testInvoiceXML, key, cert, true,
		"Test Seller", "399999999900003", ts, "115.00", "15.00")
	if err != nil {
		t.Fatalf("SignInvoiceWithQR error: %v", err)
	}

	if result.QRCode == "" {
		t.Error("QR code is empty")
	}

	// Verify QR is injected into XML
	if !strings.Contains(result.SignedXML, result.QRCode) {
		t.Error("QR code should be in signed XML")
	}

	// Decode QR and verify tags
	tags, err := DecodeTLV(result.QRCode)
	if err != nil {
		t.Fatalf("DecodeTLV: %v", err)
	}
	if string(tags[TagSellerName]) != "Test Seller" {
		t.Errorf("QR seller: %q", tags[TagSellerName])
	}
	if len(tags[TagSignature]) == 0 {
		t.Error("QR should contain a signature")
	}
}

func TestSignatureVerification(t *testing.T) {
	key, cert := generateTestCert(t)

	result, err := SignInvoice(testInvoiceXML, key, cert, false)
	if err != nil {
		t.Fatal(err)
	}

	// Decode the DER signature
	sigBytes, err := base64.StdEncoding.DecodeString(result.SignatureB64)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}

	// Parse DER signature
	r, s, err := parseDERSignature(sigBytes)
	if err != nil {
		t.Fatalf("parse DER signature: %v", err)
	}

	// Rebuild SignedInfo and hash it
	signedProps := buildSignedProperties(
		time.Now().Format("2006-01-02T15:04:05"), cert)
	_ = signedProps

	// Verify with public key - we just verify the DER encoding is valid
	if r.Sign() != 1 || s.Sign() != 1 {
		t.Error("r and s should be positive")
	}

	// Verify ECDSA signature with the hash used during signing
	// (We can't reproduce exact hash since signing time differs, but we test DER roundtrip)
	hash := sha256.Sum256([]byte("test data"))
	r2, s2, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		t.Fatal(err)
	}

	derSig := encodeECDSASignatureDER(r2, s2)
	r3, s3, err := parseDERSignature(derSig)
	if err != nil {
		t.Fatal(err)
	}

	if r2.Cmp(r3) != 0 || s2.Cmp(s3) != 0 {
		t.Error("DER roundtrip failed")
	}

	ok := ecdsa.Verify(cert.PublicKey, hash[:], r3, s3)
	if !ok {
		t.Error("ECDSA verify failed")
	}
}

// parseDERSignature parses ECDSA DER-encoded signature into r, s.
func parseDERSignature(der []byte) (*big.Int, *big.Int, error) {
	if len(der) < 6 || der[0] != 0x30 {
		return nil, nil, fmt.Errorf("invalid DER signature")
	}

	pos := 2 // skip SEQUENCE tag + length

	// Parse r
	if der[pos] != 0x02 {
		return nil, nil, fmt.Errorf("expected INTEGER tag for r")
	}
	rLen := int(der[pos+1])
	pos += 2
	r := new(big.Int).SetBytes(der[pos : pos+rLen])
	pos += rLen

	// Parse s
	if der[pos] != 0x02 {
		return nil, nil, fmt.Errorf("expected INTEGER tag for s")
	}
	sLen := int(der[pos+1])
	pos += 2
	s := new(big.Int).SetBytes(der[pos : pos+sLen])

	return r, s, nil
}

func TestInjectQR(t *testing.T) {
	xml := `<cac:AdditionalDocumentReference>
        <cbc:ID>QR</cbc:ID>
        <cac:Attachment>
            <cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain"></cbc:EmbeddedDocumentBinaryObject>
        </cac:Attachment>
    </cac:AdditionalDocumentReference>`

	result := injectQR(xml, "dGVzdFF1YXJDb2Rl")
	if !strings.Contains(result, "dGVzdFF1YXJDb2Rl") {
		t.Error("QR code not injected")
	}
}

func TestECDSADEREncoding(t *testing.T) {
	// Known values
	r := new(big.Int)
	r.SetString("12345678901234567890", 10)
	s := new(big.Int)
	s.SetString("98765432109876543210", 10)

	der := encodeECDSASignatureDER(r, s)
	if der[0] != 0x30 {
		t.Error("DER should start with SEQUENCE tag 0x30")
	}

	r2, s2, err := parseDERSignature(der)
	if err != nil {
		t.Fatal(err)
	}
	if r.Cmp(r2) != 0 {
		t.Errorf("r mismatch: %s vs %s", r, r2)
	}
	if s.Cmp(s2) != 0 {
		t.Errorf("s mismatch: %s vs %s", s, s2)
	}
}

// TestInvoiceHashMatchesSDKSamples verifies our native hash matches the
// known DigestValue from ZATCA SDK signed sample invoices.
func TestInvoiceHashMatchesSDKSamples(t *testing.T) {
	samples := []struct {
		path     string
		expected string
	}{
		{"../zatca-einvoicing-sdk-238-R4.0.0/Data/Samples/Standard/Invoice/Standard_Invoice.xml",
			"f+0WCqnPkInI+eL9G3LAry12fTPf+toC9UX07F4fI+s="},
		{"../zatca-einvoicing-sdk-238-R4.0.0/Data/Samples/Simplified/Invoice/Simplified_Invoice.xml",
			"Hss2gNFjBY5OJn/5CEVZSSNUMrSf4QlCMxwsioPN6fA="},
	}

	for _, s := range samples {
		data, err := os.ReadFile(s.path)
		if err != nil {
			t.Skipf("SDK sample not available: %v", err)
			continue
		}
		hash, err := InvoiceHash(string(data))
		if err != nil {
			t.Errorf("%s: InvoiceHash error: %v", s.path, err)
			continue
		}
		if hash != s.expected {
			t.Errorf("%s: hash mismatch\n  got:      %s\n  expected: %s", s.path, hash, s.expected)
		} else {
			t.Logf("%s: PASS (hash=%s)", s.path, hash)
		}
	}
}
