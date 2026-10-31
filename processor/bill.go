package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/models"
	"github.com/zatca-go/zatca/zatca"
)

// ProcessBill performs the full sign → submit → update pipeline for a single bill.
func ProcessBill(db *sql.DB, svc *zatca.Service, cfg *config.Config, b BillRow) (string, error) {
	log.Printf("Processing bill id=%d seq=%s", b.ID, b.SeqNumber.String)

	issueTime, err := ParseDates(b.EffectiveDate, b.PaymentDue, b.ID)
	if err != nil {
		return "", err
	}

	products, err := LoadProducts(db, b.ID)
	if err != nil {
		return "", fmt.Errorf("load products failed for bill %d: %w", b.ID, err)
	}

	inv := BuildBillInvoice(cfg, b, products, issueTime)
	return signSubmitUpdate(db, svc, inv, b.ID, "bill", xmlMeta{
		xmlDir:    cfg.XMLDir,
		tenantID:  cfg.TenantID,
		companyID: b.CompanyID,
		branchID:  b.BranchID,
		billType:  "invoice",
		qrColumn:  "qr_code",
	})
}

// ProcessBillWithChain is the ordered-stream variant. It uses the reserved
// UUID/ICV/PIH rather than allocating independent bill-worker state.
func ProcessBillWithChain(
	db *sql.DB,
	svc *zatca.Service,
	cfg *config.Config,
	b BillRow,
	chainState DocumentChainState,
) (string, error) {
	hash, _, err := ProcessBillWithChainOutcome(db, svc, cfg, b, chainState)
	return hash, err
}

// ProcessBillWithChainOutcome is the ordered-stream variant with the durable
// provider outcome returned to the EGS sequence commit.
func ProcessBillWithChainOutcome(
	db *sql.DB,
	svc *zatca.Service,
	cfg *config.Config,
	b BillRow,
	chainState DocumentChainState,
) (string, SubmissionState, error) {
	log.Printf("Processing ordered bill id=%d icv=%d", b.ID, chainState.ICV)

	issueTime, err := ParseDates(b.EffectiveDate, b.PaymentDue, b.ID)
	if err != nil {
		return "", SubmissionTerminal, err
	}
	products, err := LoadProducts(db, b.ID)
	if err != nil {
		return "", SubmissionTerminal, fmt.Errorf("load products failed for bill %d: %w", b.ID, err)
	}

	inv := BuildBillInvoiceWithChain(cfg, b, products, issueTime, chainState)
	return signSubmitUpdateOutcome(db, svc, inv, b.ID, "bill", xmlMeta{
		xmlDir:     cfg.XMLDir,
		tenantID:   cfg.TenantID,
		companyID:  b.CompanyID,
		branchID:   b.BranchID,
		billType:   "invoice",
		qrColumn:   "qr_code",
		submission: chainState.Submission,
	})
}

// BuildBillInvoice constructs an Invoice (type 388) from a BillRow.
func BuildBillInvoice(cfg *config.Config, b BillRow, products []ProductRow, issueTime time.Time) *invoice.Invoice {
	chainState := DocumentChainState{
		DocumentUUID: uuid.New().String(),
		ICV:          1,
	}
	if b.SeqNumber.Valid {
		if value, err := strconv.ParseUint(b.SeqNumber.String, 10, 64); err == nil {
			chainState.ICV = value
		}
	}
	return BuildBillInvoiceWithChain(cfg, b, products, issueTime, chainState)
}

// BuildBillInvoiceWithChain constructs an invoice using an existing EGS
// reservation. It never replaces the reserved UUID, ICV, or PIH.
func BuildBillInvoiceWithChain(
	cfg *config.Config,
	b BillRow,
	products []ProductRow,
	issueTime time.Time,
	chainState DocumentChainState,
) *invoice.Invoice {
	isStandard := b.BuyerVAT.Valid && b.BuyerVAT.String != ""
	customer := buildCustomer(b, isStandard)
	if chainState.DocumentUUID == "" {
		chainState.DocumentUUID = uuid.New().String()
	}
	if chainState.ICV == 0 {
		chainState.ICV = 1
	}

	inv := &invoice.Invoice{
		ID:               fmt.Sprintf("INV-%d", b.ID),
		UUID:             chainState.DocumentUUID,
		IssueDate:        issueTime,
		IssueTime:        issueTime,
		TypeCode:         invoice.TypeInvoice,
		DocumentCurrency: "SAR",
		TaxCurrency:      "SAR",
		Supplier: invoice.Party{
			RegistrationName:   cfg.Seller.Name,
			RegistrationNameAR: cfg.Seller.NameAR,
			VAT:                cfg.Seller.VAT,
			SchemeID:           cfg.Seller.CRN,
			ID:                 cfg.Seller.CRN,
			Street:             cfg.Seller.Street,
			Building:           cfg.Seller.Building,
			District:           cfg.Seller.District,
			City:               cfg.Seller.City,
			PostalCode:         cfg.Seller.PostalCode,
			Country:            cfg.Seller.Country,
		},
		Customer:            customer,
		PreviousInvoiceHash: chainState.effectivePIH(),
		InvoiceCounterValue: int(chainState.ICV),
		PaymentMeans: func() string {
			if b.PaymentMethod.Valid && b.PaymentMethod.String != "" {
				return b.PaymentMethod.String
			}
			return invoice.PaymentCash
		}(),
	}

	if isStandard {
		inv.SubType = invoice.SubTypeStandard
	} else {
		inv.SubType = invoice.SubTypeSimplified
	}

	if chainState.ICV == 0 && b.SeqNumber.Valid {
		if v, err := strconv.Atoi(b.SeqNumber.String); err == nil {
			inv.InvoiceCounterValue = v
		}
	}

	inv.Lines = buildLines(products)
	return inv
}

// xmlMeta holds the filesystem path components for saving signed XML.
type xmlMeta struct {
	xmlDir     string // base directory (e.g. /data/zatca-xml)
	tenantID   string // tenant identifier (db_name) - prevents cross-tenant collisions
	companyID  int64
	branchID   int64
	billType   string // "invoice", "credit", "debit"
	qrColumn   string // DB column name for QR code ("qr_code" for bill, "invoice_qr" for credit_note)
	submission SubmissionIdentity
}

// signSubmitUpdate is the shared sign → submit → QR → save XML → UPDATE pipeline used by all doc types.
func signSubmitUpdate(db *sql.DB, svc *zatca.Service, inv *invoice.Invoice, rowID int64, table string, meta xmlMeta) (string, error) {
	hash, _, err := signSubmitUpdateOutcome(db, svc, inv, rowID, table, meta)
	return hash, err
}

func signSubmitUpdateOutcome(
	db *sql.DB,
	svc *zatca.Service,
	inv *invoice.Invoice,
	rowID int64,
	table string,
	meta xmlMeta,
) (string, SubmissionState, error) {
	if err := meta.submission.Validate(); err != nil {
		return "", SubmissionTerminal, fmt.Errorf("provider submission requires canonical identity: %w", err)
	}
	if rowID <= 0 || meta.submission.SourceID != uint64(rowID) {
		return "", SubmissionTerminal, fmt.Errorf("provider source_id %d does not match %s row %d",
			meta.submission.SourceID, table, rowID)
	}
	if expected := documentTable(meta.submission.DocumentType); expected != table {
		return "", SubmissionTerminal, fmt.Errorf("provider document_type %q cannot persist to %s",
			meta.submission.DocumentType, table)
	}

	ctx := context.Background()
	store := NewSQLSubmissionStore(db)
	latest, found, err := store.Latest(ctx, meta.submission)
	if err != nil {
		return "", SubmissionTerminal, fmt.Errorf("load provider submission state: %w", err)
	}
	engine := NewSubmissionEngine(newServiceSubmissionProvider(svc), store)
	if found {
		request := SubmissionRequest{
			Identity:        meta.submission,
			Standard:        inv.IsStandard(),
			SignedRequest:   latest.SignedRequest,
			ProviderRequest: latest.RequestArtifact,
			InvoiceHash:     latest.InvoiceHash,
		}
		result, submitErr := engine.Submit(ctx, request)
		if submitErr != nil {
			return "", result.Attempt.State, submitErr
		}
		if !result.Attempt.successful() {
			return "", result.Attempt.State, result.Attempt.outcomeError()
		}
		if err := applyAcceptedProjection(db, svc, result.Attempt, rowID, table, meta, signedXMLPath(meta, rowID)); err != nil {
			return "", result.Attempt.State, err
		}
		return result.Attempt.InvoiceHash, result.Attempt.State, nil
	}

	xmlStr, err := inv.ToXML()
	if err != nil {
		return "", SubmissionTerminal, fmt.Errorf("ToXML failed: %w", err)
	}

	signedXML, hash, err := svc.SDK.SignInvoice(xmlStr)
	if err != nil {
		return "", SubmissionTerminal, fmt.Errorf("SignInvoice failed: %w", err)
	}

	xmlPath := saveSignedXML(meta, rowID, signedXML)

	apiJSON, err := svc.SDK.GenerateAPIRequest(signedXML)
	if err != nil {
		return "", SubmissionTerminal, fmt.Errorf("GenerateAPIRequest failed: %w", err)
	}

	var apiReq models.InvoiceRequest
	if err := json.Unmarshal([]byte(apiJSON), &apiReq); err != nil {
		return "", SubmissionTerminal, fmt.Errorf("parse api json failed: %w", err)
	}
	if apiReq.UUID != "" && apiReq.UUID != meta.submission.InvoiceUUID {
		return "", SubmissionTerminal, fmt.Errorf("provider request UUID %q does not match canonical invoice_uuid %q", apiReq.UUID, meta.submission.InvoiceUUID)
	}

	qr := ""
	if signedXML != "" {
		if q, qerr := svc.SDK.GenerateQR(signedXML); qerr == nil {
			qr = q
		} else {
			log.Printf("DEBUG: GenerateQR failed: %v", qerr)
		}
	}

	finalHash := apiReq.InvoiceHash
	if finalHash == "" {
		finalHash = hash
	}

	log.Printf("DEBUG: provider submit request UUID=%s hash=%s", apiReq.UUID, finalHash)
	result, submitErr := engine.Submit(ctx, SubmissionRequest{
		Identity:        meta.submission,
		Standard:        inv.IsStandard(),
		SignedRequest:   []byte(signedXML),
		ProviderRequest: []byte(apiJSON),
		InvoiceHash:     finalHash,
		QRCode:          []byte(qr),
	})
	if submitErr != nil {
		return "", result.Attempt.State, submitErr
	}
	if !result.Attempt.successful() {
		return "", result.Attempt.State, result.Attempt.outcomeError()
	}
	if result.Attempt.InvoiceHash == "" {
		return "", SubmissionTerminal, fmt.Errorf("no invoice hash returned for %s %d", table, rowID)
	}
	if err := applyAcceptedProjection(db, svc, result.Attempt, rowID, table, meta, xmlPath); err != nil {
		return "", result.Attempt.State, err
	}
	return result.Attempt.InvoiceHash, result.Attempt.State, nil
}

func applyAcceptedProjection(
	db *sql.DB,
	svc *zatca.Service,
	attempt SubmissionAttempt,
	rowID int64,
	table string,
	meta xmlMeta,
	xmlPath string,
) error {
	if !attempt.successful() {
		return fmt.Errorf("refusing to issue non-successful provider outcome %q", attempt.State)
	}
	if attempt.InvoiceHash == "" {
		return errors.New("accepted provider outcome is missing invoice hash")
	}
	if attempt.Identity.SourceID != uint64(rowID) {
		return fmt.Errorf("accepted provider source_id %d does not match %s row %d",
			attempt.Identity.SourceID, table, rowID)
	}
	if expected := documentTable(attempt.Identity.DocumentType); expected != table {
		return fmt.Errorf("accepted provider document_type %q cannot persist to %s",
			attempt.Identity.DocumentType, table)
	}
	qr := ""
	if svc != nil && svc.SDK != nil && len(attempt.SignedRequest) > 0 {
		if value, err := svc.SDK.GenerateQR(string(attempt.SignedRequest)); err == nil {
			qr = value
		}
	}
	qrCol := meta.qrColumn
	if qrCol == "" {
		qrCol = "qr_code"
	}
	updateQ := fmt.Sprintf(`UPDATE %s SET invoice_hash = ?, %s = ?, invoice_uuid = ?, invoice_xml_path = COALESCE(NULLIF(?, ''), invoice_xml_path), state = 3 WHERE id = ?`, table, qrCol)
	if _, err := db.Exec(updateQ, attempt.InvoiceHash, qr, attempt.Identity.InvoiceUUID, xmlPath, rowID); err != nil {
		return fmt.Errorf("update metadata after provider acceptance failed for %s %d: %w", table, rowID, err)
	}
	return nil
}

func documentTable(documentType string) string {
	switch documentType {
	case "bill":
		return "bill"
	case "credit":
		return "credit_note"
	case "debit":
		return "debit_note"
	default:
		return ""
	}
}

func signedXMLPath(meta xmlMeta, rowID int64) string {
	if meta.xmlDir == "" {
		return ""
	}
	tenant := meta.tenantID
	if tenant == "" {
		tenant = "_default"
	}
	return filepath.Join(meta.xmlDir, tenant, fmt.Sprintf("%d", meta.companyID),
		fmt.Sprintf("%d", meta.branchID), meta.billType, fmt.Sprintf("%d.xml", rowID))
}

func saveSignedXML(meta xmlMeta, rowID int64, signedXML string) string {
	xmlPath := signedXMLPath(meta, rowID)
	if xmlPath == "" {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(xmlPath), 0755); err != nil {
		log.Printf("WARN: mkdir %s: %v", filepath.Dir(xmlPath), err)
		return ""
	}
	if err := os.WriteFile(xmlPath, []byte(signedXML), 0644); err != nil {
		log.Printf("WARN: write xml %s: %v", xmlPath, err)
		return ""
	}
	log.Printf("  Saved signed XML: %s", xmlPath)
	return xmlPath
}

// buildCustomer builds the Customer party from BillRow.
func buildCustomer(b BillRow, isStandard bool) invoice.Party {
	customer := invoice.Party{
		RegistrationName: func() string {
			if b.UserName.Valid {
				return b.UserName.String
			}
			return "Customer"
		}(),
		Country: "SA",
	}

	if isStandard {
		customer.VAT = b.BuyerVAT.String
		if b.BuyerCompany.Valid && b.BuyerCompany.String != "" {
			customer.RegistrationName = b.BuyerCompany.String
		} else if b.BuyerName.Valid && b.BuyerName.String != "" {
			customer.RegistrationName = b.BuyerName.String
		}
		if b.BuyerStreet.Valid && b.BuyerStreet.String != "" {
			customer.Street = b.BuyerStreet.String
		} else if b.BuyerAddress.Valid && b.BuyerAddress.String != "" {
			customer.Street = b.BuyerAddress.String
		}
		if b.BuyerBuilding.Valid && b.BuyerBuilding.String != "" {
			customer.Building = b.BuyerBuilding.String
		}
		if b.BuyerDistrict.Valid && b.BuyerDistrict.String != "" {
			customer.District = b.BuyerDistrict.String
		}
		if b.BuyerCity.Valid && b.BuyerCity.String != "" {
			customer.City = b.BuyerCity.String
		}
		if b.BuyerPostalCode.Valid && b.BuyerPostalCode.String != "" {
			customer.PostalCode = b.BuyerPostalCode.String
		}
		if b.BuyerCountry.Valid && b.BuyerCountry.String != "" {
			customer.Country = b.BuyerCountry.String
		}
		if b.BuyerSchemeID.Valid && b.BuyerSchemeID.String != "" {
			customer.SchemeID = b.BuyerSchemeID.String
		}
		if b.BuyerRegistration.Valid && b.BuyerRegistration.String != "" {
			customer.ID = b.BuyerRegistration.String
		}
	}
	return customer
}

// buildLines converts ProductRows into invoice LineItems.
func buildLines(products []ProductRow) []invoice.LineItem {
	var lines []invoice.LineItem
	for i, p := range products {
		lineName := "product"
		if p.Name.Valid && p.Name.String != "" {
			lineName = p.Name.String
		} else if p.ProductID.Valid {
			lineName = fmt.Sprintf("product-%d", p.ProductID.Int64)
		}

		li := invoice.LineItem{
			ID:          fmt.Sprintf("%d", i+1),
			Name:        lineName,
			Quantity:    p.QuantityOrZero(),
			UnitCode:    "PCE",
			UnitPrice:   p.PriceOrZero(),
			Discount:    0,
			TaxCategory: invoice.TaxStandard,
			TaxPercent:  p.VATOrZero(),
		}
		lines = append(lines, li)
	}
	return lines
}
