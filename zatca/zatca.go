package zatca

import (
	"encoding/base64"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/zatca-go/zatca/client"
	"github.com/zatca-go/zatca/config"
	zcrypto "github.com/zatca-go/zatca/crypto"
	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/models"
	"github.com/zatca-go/zatca/sdk"
)

// Service orchestrates ZATCA e-invoicing operations.
type Service struct {
	Config *config.Config
	Client *client.Client
	SDK    *sdk.SDK
}

// New creates a new ZATCA service from configuration.
func New(cfg *config.Config) *Service {
	return &Service{
		Config: cfg,
		Client: client.New(cfg),
		SDK:    sdk.New(cfg),
	}
}

// --- Credentials ---

// Credentials holds ZATCA API credentials obtained during onboarding.
type Credentials struct {
	Certificate string // Base64-encoded X.509 certificate (BinarySecurityToken)
	Secret      string // API secret/password
	RequestID   string // Request ID from ZATCA
	PrivateKey  string // PEM-encoded private key (from CSR generation)
}

// --- Flow A: Onboarding ---

// OnboardResult holds the result of the complete onboarding flow.
type OnboardResult struct {
	CSR                 string
	PrivateKey          string
	ComplianceCert      string
	ComplianceSecret    string
	ComplianceRequestID string
	ProductionCert      string
	ProductionSecret    string
	ProductionRequestID string
}

// Onboard performs the complete ZATCA onboarding flow:
// 1. Generate CSR via SDK Docker
// 2. Request Compliance CSID from ZATCA
// 3. Submit 6 compliance invoices (standard + simplified × invoice/credit/debit)
// 4. Request Production CSID from ZATCA
func (s *Service) Onboard() (*OnboardResult, error) {
	log.Printf("Onboard: env=%s baseURL=%s otp=%s", s.Config.Env, s.Config.BaseURL(), maskOTP(s.Config.OTP))

	// Step 1: Generate CSR
	log.Printf("Onboard step 1/4: generating CSR (CommonName=%s OrgUnit=%s)", s.Config.CSR.CommonName, s.Config.CSR.OrgUnit)
	csrResult, err := s.SDK.GenerateCSR(s.Config.CSR)
	if err != nil {
		return nil, fmt.Errorf("step 1 (CSR): %w", err)
	}
	log.Printf("Onboard step 1/4: OK — CSR len=%d, private key len=%d", len(csrResult.CSR), len(csrResult.PrivateKey))

	res, err := s.OnboardWithCSR(csrResult.CSR, csrResult.PrivateKey)
	if err != nil {
		return nil, err
	}
	res.CSR = csrResult.CSR
	res.PrivateKey = csrResult.PrivateKey
	return res, nil
}

// OnboardWithCSR runs steps 2–4 of onboarding using a CSR and private key
// that have already been generated (typically loaded from the database
// after a previous failed attempt). Steps:
//
//  2. Request Compliance CSID from ZATCA
//  3. Submit 6 compliance invoices
//  4. Request Production CSID from ZATCA
//
// The CSR + private key are NOT included in the returned OnboardResult
// so the caller is expected to know them already.
func (s *Service) OnboardWithCSR(csrPEM, privateKeyPEM string) (*OnboardResult, error) {
	result := &OnboardResult{}

	// Inject the private key into the SDK so subsequent Docker commands can sign.
	s.SDK.SetCredentials(privateKeyPEM, "")

	// Step 2: Request Compliance CSID
	csrBase64 := pemToBase64(csrPEM)
	log.Printf("Onboard step 2/4: requesting compliance CSID (URL=%s/compliance)", s.Config.BaseURL())
	compResp, err := s.Client.ComplianceCSID(csrBase64)
	if err != nil {
		return nil, fmt.Errorf("step 2 (compliance CSID): %w", err)
	}
	log.Printf("Onboard step 2/4: OK — requestID=%s cert_len=%d", compResp.RequestID.String(), len(compResp.BinarySecurityToken))
	result.ComplianceCert = compResp.BinarySecurityToken
	result.ComplianceSecret = compResp.Secret
	result.ComplianceRequestID = compResp.RequestID.String()

	// Update client credentials for compliance checks
	s.Config.ComplianceUsername = compResp.BinarySecurityToken
	s.Config.CompliancePassword = compResp.Secret

	// Update SDK with the compliance certificate for signing.
	//
	// ZATCA's /compliance and /production/csids endpoints return
	// `binarySecurityToken` as Base64(<cert.pem body>), where the inner body is
	// itself the raw Base64(DER) X.509 certificate (the same shape as the SDK's
	// shipped Data/Certificates/cert.pem). I.e. the token on the wire is
	// double-encoded. The Java SDK's InvoiceSigningService reads cert.pem and
	// calls Base64.getDecoder().decode(...) once, then
	// CertificateFactory.generateCertificate(...) on the result — so cert.pem
	// must contain Base64(DER), NOT Base64(Base64(DER)).
	//
	// Reproduced in Docker (zatca-test image, fatoora -sign):
	//   - writing the raw token (still doubly-encoded) → fails with
	//     "[ERROR] InvoiceSigningService - failed to sign invoice
	//      [please provide a valid certificate]"
	//   - writing base64-decoded(token) → signs successfully.
	//
	// Reference: ZATCA Discourse, "Decoding Binary Security Token":
	//   "Before signing any B2C invoice ensure to decode the BinarySecurityToken
	//    using Base64 and put the decoded BinarySecurityToken to the cert.pem".
	decodedCert, err := base64.StdEncoding.DecodeString(compResp.BinarySecurityToken)
	if err != nil {
		return nil, fmt.Errorf("step 2: base64-decode binarySecurityToken (len=%d): %w", len(compResp.BinarySecurityToken), err)
	}
	certPEM := string(decodedCert)
	log.Printf("Onboard step 2/4: cert decoded (token_len=%d → cert_len=%d, first16=%q)",
		len(compResp.BinarySecurityToken), len(certPEM), safePrefix(certPEM, 16))
	s.SDK.SetCredentials(privateKeyPEM, certPEM)

	// Step 3: Submit 6 compliance invoices
	log.Printf("Onboard step 3/4: submitting 6 compliance invoices")
	if err := s.submitComplianceInvoices(privateKeyPEM, compResp.BinarySecurityToken); err != nil {
		return nil, fmt.Errorf("step 3 (compliance invoices): %w", err)
	}
	log.Printf("Onboard step 3/4: OK — all 6 compliance invoices passed")

	// Step 4: Request Production CSID
	log.Printf("Onboard step 4/4: requesting production CSID (requestID=%s)", compResp.RequestID.String())
	prodResp, err := s.Client.ProductionCSID(compResp.RequestID.String())
	if err != nil {
		return nil, fmt.Errorf("step 4 (production CSID): %w", err)
	}
	log.Printf("Onboard step 4/4: OK — production cert received (len=%d)", len(prodResp.BinarySecurityToken))
	result.ProductionCert = prodResp.BinarySecurityToken
	result.ProductionSecret = prodResp.Secret
	result.ProductionRequestID = prodResp.RequestID.String()

	// Update client credentials
	s.Config.ProductionUsername = prodResp.BinarySecurityToken
	s.Config.ProductionPassword = prodResp.Secret

	return result, nil
}

// submitComplianceInvoices submits the 6 required compliance invoices.
func (s *Service) submitComplianceInvoices(privateKeyPEM, certBase64 string) error {
	// Parse credentials for signing
	privKey, err := zcrypto.ParsePrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	cert, err := zcrypto.ParseCertificate(certBase64)
	if err != nil {
		return fmt.Errorf("parse certificate: %w", err)
	}

	// Generate 6 test invoices: [standard, simplified] × [invoice, credit, debit]
	types := []struct {
		typeCode invoice.TypeCode
		subType  invoice.SubType
		name     string
	}{
		{invoice.TypeInvoice, invoice.SubTypeStandard, "Standard Invoice"},
		{invoice.TypeInvoice, invoice.SubTypeSimplified, "Simplified Invoice"},
		{invoice.TypeCreditNote, invoice.SubTypeStandard, "Standard Credit Note"},
		{invoice.TypeCreditNote, invoice.SubTypeSimplified, "Simplified Credit Note"},
		{invoice.TypeDebitNote, invoice.SubTypeStandard, "Standard Debit Note"},
		{invoice.TypeDebitNote, invoice.SubTypeSimplified, "Simplified Debit Note"},
	}

	pih := "NWZlY2ViNjZmZmM4NmYzOGQ5NTI3ODZjNmQ2OTZjNzljMmRiYzIzOWRkNGU5MWI0NjcyOWQ3M2EyN2ZiNTdlOQ=="
	icv := 1

	for i, t := range types {
		log.Printf("  Compliance invoice %d/6: %s", i+1, t.name)
		inv := s.buildComplianceInvoice(t.typeCode, t.subType, icv, pih)

		signResult, err := s.signAndSubmitCompliance(inv, privKey, cert)
		if err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
		log.Printf("  Compliance invoice %d/6: %s — OK", i+1, t.name)
		pih = signResult.InvoiceHash
		icv++
	}

	return nil
}

// buildComplianceInvoice creates a test invoice for compliance checking.
func (s *Service) buildComplianceInvoice(typeCode invoice.TypeCode, subType invoice.SubType, icv int, pih string) *invoice.Invoice {
	now := time.Now().UTC()
	inv := &invoice.Invoice{
		ID:        fmt.Sprintf("COMP-%06d", icv),
		UUID:      fmt.Sprintf("00000000-0000-0000-0000-%012d", icv),
		IssueDate: now,
		IssueTime: now,
		TypeCode:  typeCode,
		SubType:   subType,
		Supplier: invoice.Party{
			RegistrationName: s.Config.Seller.Name,
			VAT:              s.Config.Seller.VAT,
			SchemeID:         "CRN",
			ID:               s.Config.Seller.CRN,
			Street:           s.Config.Seller.Street,
			Building:         s.Config.Seller.Building,
			District:         s.Config.Seller.District,
			City:             s.Config.Seller.City,
			PostalCode:       s.Config.Seller.PostalCode,
			Country:          s.Config.Seller.Country,
		},
		PaymentMeans:        invoice.PaymentCash,
		PreviousInvoiceHash: pih,
		InvoiceCounterValue: icv,
		Lines: []invoice.LineItem{
			{
				Name:        "Compliance Test Item",
				Quantity:    1,
				UnitCode:    "PCE",
				UnitPrice:   100.00,
				TaxCategory: invoice.TaxStandard,
				TaxPercent:  15.00,
			},
		},
	}

	// Standard (B2B) invoices require a Customer party with VAT.
	// Simplified (B2C) compliance invoices MUST NOT include a customer
	// with VAT/address (KSA-22): leave Customer empty so the XML omits
	// the AccountingCustomerParty block.
	if subType == invoice.SubTypeStandard {
		inv.Customer = invoice.Party{
			RegistrationName: "Fatoora Samples LTD",
			VAT:              "399999999800003",
			Street:           "Prince Sultan",
			Building:         "2322",
			District:         "Al-Murabba",
			City:             "Riyadh",
			PostalCode:       "23333",
			Country:          "SA",
		}
	}

	// Credit/debit notes need a billing reference and KSA-10 reason note.
	if typeCode == invoice.TypeCreditNote || typeCode == invoice.TypeDebitNote {
		inv.BillingReferenceID = "COMP-000001"
		inv.InstructionNote = "Compliance test correction"
		// cbc:Note is also required on credit/debit notes per KSA-10.
		inv.Note = "Compliance test correction"
		inv.NoteLang = "en"
	}

	return inv
}

// signAndSubmitCompliance signs an invoice and submits it for compliance check.
func (s *Service) signAndSubmitCompliance(inv *invoice.Invoice, privKey interface{}, cert *zcrypto.CertInfo) (*zcrypto.SignResult, error) {
	// Generate XML
	xmlStr, err := inv.ToXML()
	if err != nil {
		return nil, fmt.Errorf("generate XML: %w", err)
	}
	log.Printf("    XML generated (len=%d), signing via SDK...", len(xmlStr))

	// Sign using SDK Docker (most reliable for compliance)
	signedXML, hash, err := s.SDK.SignInvoice(xmlStr)
	if err != nil {
		return nil, fmt.Errorf("sign invoice: %w", err)
	}
	log.Printf("    Signed OK (len=%d, hash=%s...)", len(signedXML), hash[:min(16, len(hash))])

	// Submit for compliance check
	invoiceB64 := base64.StdEncoding.EncodeToString([]byte(signedXML))
	req := models.InvoiceRequest{
		InvoiceHash: hash,
		UUID:        inv.UUID,
		Invoice:     invoiceB64,
	}

	resp, err := s.Client.ComplianceCheck(req)
	if err != nil {
		return nil, fmt.Errorf("compliance check: %w", err)
	}

	if resp.ValidationResults != nil && len(resp.ValidationResults.ErrorMessages) > 0 {
		return nil, fmt.Errorf("compliance validation errors: %v", resp.ValidationResults.ErrorMessages)
	}

	return &zcrypto.SignResult{
		SignedXML:   signedXML,
		InvoiceHash: hash,
	}, nil
}

// --- Flow B: Report Simplified Invoice (B2C) ---

// ReportResult holds the result of reporting an invoice.
type ReportResult struct {
	SignedXML   string
	InvoiceHash string
	QRCode      string
	Status      string
	Warnings    []models.ErrorDetail
}

// ReportInvoice builds, signs, and reports a simplified invoice (B2C).
// Uses SDK Docker for signing.
func (s *Service) ReportInvoice(inv *invoice.Invoice) (*ReportResult, error) {
	if !inv.IsSimplified() {
		return nil, fmt.Errorf("ReportInvoice is for simplified (B2C) invoices only")
	}

	// Generate XML
	xmlStr, err := inv.ToXML()
	if err != nil {
		return nil, fmt.Errorf("generate XML: %w", err)
	}

	// Sign via SDK
	signedXML, hash, err := s.SDK.SignInvoice(xmlStr)
	if err != nil {
		return nil, fmt.Errorf("sign invoice: %w", err)
	}

	// Submit to reporting API
	invoiceB64 := base64.StdEncoding.EncodeToString([]byte(signedXML))
	req := models.InvoiceRequest{
		InvoiceHash: hash,
		UUID:        inv.UUID,
		Invoice:     invoiceB64,
	}
	resp, err := s.Client.ReportInvoice(req)
	if err != nil {
		return nil, fmt.Errorf("report invoice: %w", err)
	}

	return &ReportResult{
		SignedXML:   signedXML,
		InvoiceHash: hash,
		Status:      resp.Status,
		Warnings:    resp.Warnings,
	}, nil
}

// ReportInvoiceNative builds, signs natively (no Docker), and reports.
func (s *Service) ReportInvoiceNative(inv *invoice.Invoice, privateKeyPEM, certBase64 string) (*ReportResult, error) {
	if !inv.IsSimplified() {
		return nil, fmt.Errorf("ReportInvoice is for simplified (B2C) invoices only")
	}

	privKey, err := zcrypto.ParsePrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	cert, err := zcrypto.ParseCertificate(certBase64)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}

	// Generate XML
	xmlStr, err := inv.ToXML()
	if err != nil {
		return nil, fmt.Errorf("generate XML: %w", err)
	}

	// Sign natively
	signResult, err := zcrypto.SignInvoiceWithQR(
		xmlStr, privKey, cert, true,
		inv.Supplier.RegistrationName, inv.Supplier.VAT,
		inv.IssueDate, fmt.Sprintf("%.2f", inv.TotalWithTax()), fmt.Sprintf("%.2f", inv.TotalTax()),
	)
	if err != nil {
		return nil, fmt.Errorf("sign invoice: %w", err)
	}

	// Submit to reporting API
	invoiceB64 := base64.StdEncoding.EncodeToString([]byte(signResult.SignedXML))
	req := models.InvoiceRequest{
		InvoiceHash: signResult.InvoiceHash,
		UUID:        inv.UUID,
		Invoice:     invoiceB64,
	}
	resp, err := s.Client.ReportInvoice(req)
	if err != nil {
		return nil, fmt.Errorf("report invoice: %w", err)
	}

	return &ReportResult{
		SignedXML:   signResult.SignedXML,
		InvoiceHash: signResult.InvoiceHash,
		QRCode:      signResult.QRCode,
		Status:      resp.Status,
		Warnings:    resp.Warnings,
	}, nil
}

// --- Flow C: Clear Standard Invoice (B2B) ---

// ClearResult holds the result of clearing an invoice.
type ClearResult struct {
	SignedXML   string
	ClearedXML  string // XML returned by ZATCA after clearance
	InvoiceHash string
	Status      string
	Warnings    []models.ErrorDetail
}

// ClearInvoice builds, signs, and clears a standard invoice (B2B).
// Uses SDK Docker for signing.
func (s *Service) ClearInvoice(inv *invoice.Invoice) (*ClearResult, error) {
	if !inv.IsStandard() {
		return nil, fmt.Errorf("ClearInvoice is for standard (B2B) invoices only")
	}

	// Generate XML
	xmlStr, err := inv.ToXML()
	if err != nil {
		return nil, fmt.Errorf("generate XML: %w", err)
	}

	// Sign via SDK
	signedXML, hash, err := s.SDK.SignInvoice(xmlStr)
	if err != nil {
		return nil, fmt.Errorf("sign invoice: %w", err)
	}

	// Submit to clearance API
	invoiceB64 := base64.StdEncoding.EncodeToString([]byte(signedXML))
	req := models.InvoiceRequest{
		InvoiceHash: hash,
		UUID:        inv.UUID,
		Invoice:     invoiceB64,
	}
	resp, err := s.Client.ClearInvoice(req)
	if err != nil {
		return nil, fmt.Errorf("clear invoice: %w", err)
	}

	// Decode cleared invoice if provided
	var clearedXML string
	if resp.ClearedInvoice != "" {
		decoded, err := base64.StdEncoding.DecodeString(resp.ClearedInvoice)
		if err == nil {
			clearedXML = string(decoded)
		}
	}

	return &ClearResult{
		SignedXML:   signedXML,
		ClearedXML:  clearedXML,
		InvoiceHash: hash,
		Status:      resp.Status,
		Warnings:    resp.Warnings,
	}, nil
}

// ClearInvoiceNative builds, signs natively (no Docker), and clears.
func (s *Service) ClearInvoiceNative(inv *invoice.Invoice, privateKeyPEM, certBase64 string) (*ClearResult, error) {
	if !inv.IsStandard() {
		return nil, fmt.Errorf("ClearInvoice is for standard (B2B) invoices only")
	}

	privKey, err := zcrypto.ParsePrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	cert, err := zcrypto.ParseCertificate(certBase64)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}

	xmlStr, err := inv.ToXML()
	if err != nil {
		return nil, fmt.Errorf("generate XML: %w", err)
	}

	signResult, err := zcrypto.SignInvoiceWithQR(
		xmlStr, privKey, cert, false,
		inv.Supplier.RegistrationName, inv.Supplier.VAT,
		inv.IssueDate, fmt.Sprintf("%.2f", inv.TotalWithTax()), fmt.Sprintf("%.2f", inv.TotalTax()),
	)
	if err != nil {
		return nil, fmt.Errorf("sign invoice: %w", err)
	}

	invoiceB64 := base64.StdEncoding.EncodeToString([]byte(signResult.SignedXML))
	req := models.InvoiceRequest{
		InvoiceHash: signResult.InvoiceHash,
		UUID:        inv.UUID,
		Invoice:     invoiceB64,
	}
	resp, err := s.Client.ClearInvoice(req)
	if err != nil {
		return nil, fmt.Errorf("clear invoice: %w", err)
	}

	var clearedXML string
	if resp.ClearedInvoice != "" {
		decoded, err := base64.StdEncoding.DecodeString(resp.ClearedInvoice)
		if err == nil {
			clearedXML = string(decoded)
		}
	}

	return &ClearResult{
		SignedXML:   signResult.SignedXML,
		ClearedXML:  clearedXML,
		InvoiceHash: signResult.InvoiceHash,
		Status:      resp.Status,
		Warnings:    resp.Warnings,
	}, nil
}

// --- Helper ---

// pemToBase64 base64-encodes the entire PEM content, as required by ZATCA API.
func pemToBase64(pemStr string) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(pemStr)))
}

// maskOTP shows first 2 and last 2 chars, masking the rest.
func maskOTP(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[:2] + "****" + s[len(s)-2:]
}

// safePrefix returns the first n bytes of s, or s itself if shorter.
// Used for safe diagnostic logging of cert/key bodies.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
