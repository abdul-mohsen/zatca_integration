package processor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/models"
	"github.com/zatca-go/zatca/zatca"
)

// ProcessBill performs the full sign → submit → update pipeline for a single bill.
func ProcessBill(db *sql.DB, svc *zatca.Service, cfg *config.Config, b BillRow) (string, error) {
	return processBillWithChainStore(db, NewSQLChainStateStore(db), svc, cfg, b)
}

func processBillWithChainStore(db *sql.DB, chainStore ChainStateStore, svc *zatca.Service, cfg *config.Config, b BillRow) (string, error) {
	log.Printf("Processing bill id=%d seq=%s", b.ID, b.SeqNumber.String)

	issueTime, err := ParseDates(b.EffectiveDate, b.PaymentDue, b.ID)
	if err != nil {
		return "", err
	}

	products, err := LoadProducts(db, b.ID)
	if err != nil {
		return "", fmt.Errorf("load products failed for bill %d: %w", b.ID, err)
	}

	position, err := reserveChainPosition(chainStore, ChainDocument{
		BranchID:        b.BranchID,
		Type:            DocumentBill,
		DocumentID:      b.ID,
		ProposedUUID:    uuid.New().String(),
		ProposedIssueAt: issueTime,
	})
	if err != nil {
		return "", err
	}
	if position.Status == chainPositionAccepted {
		return position.InvoiceHash, nil
	}

	inv := BuildBillInvoice(cfg, b, products, issueTime)
	if err := applyChainPosition(inv, position); err != nil {
		return "", err
	}
	return signSubmitUpdate(db, svc, inv, b.ID, "bill", xmlMeta{
		xmlDir:    cfg.XMLDir,
		tenantID:  cfg.TenantID,
		companyID: b.CompanyID,
		branchID:  b.BranchID,
		billType:  "invoice",
		qrColumn:  "qr_code",
	}, chainStore, position)
}

// BuildBillInvoice constructs an Invoice (type 388) from a BillRow.
func BuildBillInvoice(cfg *config.Config, b BillRow, products []ProductRow, issueTime time.Time) *invoice.Invoice {
	isStandard := b.BuyerVAT.Valid && b.BuyerVAT.String != ""
	customer := buildCustomer(b, isStandard)

	inv := &invoice.Invoice{
		ID:               fmt.Sprintf("INV-%d", b.ID),
		UUID:             uuid.New().String(),
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
		Customer: customer,
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

	inv.Lines = buildLines(products)
	return inv
}

// xmlMeta holds the filesystem path components for saving signed XML.
type xmlMeta struct {
	xmlDir    string // base directory (e.g. /data/zatca-xml)
	tenantID  string // tenant identifier (db_name) - prevents cross-tenant collisions
	companyID int64
	branchID  int64
	billType  string // "invoice", "credit", "debit"
	qrColumn  string // DB column name for QR code ("qr_code" for bill, "invoice_qr" for credit_note)
}

// signSubmitUpdate is the shared sign → submit → QR → save XML → UPDATE pipeline used by all doc types.
func signSubmitUpdate(db *sql.DB, svc *zatca.Service, inv *invoice.Invoice, rowID int64, table string, meta xmlMeta, chainStore ChainStateStore, position ChainPosition) (string, error) {
	var (
		signedXML = position.SignedXML
		hash      = position.InvoiceHash
		err       error
	)
	if signedXML == "" {
		xmlStr, xmlErr := inv.ToXML()
		if xmlErr != nil {
			return "", fmt.Errorf("ToXML failed: %w", xmlErr)
		}
		signedXML, hash, err = svc.SDK.SignInvoice(xmlStr)
		if err != nil {
			return "", fmt.Errorf("SignInvoice failed: %w", err)
		}
		if err := chainStore.SavePrepared(position, signedXML, hash); err != nil {
			return "", fmt.Errorf("persist prepared chain position: %w", err)
		}
	}
	if signedXML == "" || hash == "" {
		return "", fmt.Errorf("prepared signed XML and invoice hash are required")
	}

	// Save signed XML to filesystem:
	//   {xmlDir}/{tenant}/{company_id}/{branch_id}/{bill_type}/{rowID}.xml
	// Tenant prefix prevents collisions when multiple tenants share the
	// same xml-dir mount.
	var xmlPath string
	if meta.xmlDir != "" {
		tenant := meta.tenantID
		if tenant == "" {
			tenant = "_default"
		}
		dir := filepath.Join(meta.xmlDir, tenant, fmt.Sprintf("%d", meta.companyID), fmt.Sprintf("%d", meta.branchID), meta.billType)
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Printf("WARN: mkdir %s: %v", dir, err)
		} else {
			xmlPath = filepath.Join(dir, fmt.Sprintf("%d.xml", rowID))
			if err := os.WriteFile(xmlPath, []byte(signedXML), 0644); err != nil {
				log.Printf("WARN: write xml %s: %v", xmlPath, err)
				xmlPath = "" // don't store path if write failed
			} else {
				log.Printf("  Saved signed XML: %s", xmlPath)
			}
		}
	}

	apiJSON, err := svc.SDK.GenerateAPIRequest(signedXML)
	if err != nil {
		return "", fmt.Errorf("GenerateAPIRequest failed: %w", err)
	}

	var apiReq models.InvoiceRequest
	if err := json.Unmarshal([]byte(apiJSON), &apiReq); err != nil {
		return "", fmt.Errorf("parse api json failed: %w", err)
	}
	if apiReq.InvoiceHash != "" && apiReq.InvoiceHash != hash {
		return "", fmt.Errorf("prepared invoice hash %q differs from API request hash %q", hash, apiReq.InvoiceHash)
	}
	if apiReq.InvoiceHash == "" {
		apiReq.InvoiceHash = hash
	}
	if apiReq.UUID != "" && apiReq.UUID != position.InvoiceUUID {
		return "", fmt.Errorf("prepared invoice UUID %q differs from API request UUID %q", position.InvoiceUUID, apiReq.UUID)
	}
	if apiReq.UUID == "" {
		apiReq.UUID = position.InvoiceUUID
	}

	var resp *models.InvoiceResponse
	if inv.IsStandard() {
		log.Printf("DEBUG: ClearInvoice request UUID=%s hash=%s", apiReq.UUID, apiReq.InvoiceHash)
		resp, err = svc.Client.ClearInvoice(apiReq)
		if err != nil {
			return "", fmt.Errorf("ClearInvoice API failed: %v", err)
		}
	} else {
		log.Printf("DEBUG: ReportInvoice request UUID=%s hash=%s", apiReq.UUID, apiReq.InvoiceHash)
		resp, err = svc.Client.ReportInvoice(apiReq)
		if err != nil {
			return "", fmt.Errorf("ReportInvoice API failed: %v", err)
		}
	}
	if resp == nil {
		return "", fmt.Errorf("invoice API returned nil response")
	}
	if !providerResponseAccepted(resp) {
		return "", fmt.Errorf("invoice API did not accept %s %d: status=%q", meta.billType, rowID, resp.Status)
	}

	log.Printf("  Result: status=%s, hash=%s", resp.Status, apiReq.InvoiceHash)

	qr := ""
	if signedXML != "" {
		if q, qerr := svc.SDK.GenerateQR(signedXML); qerr == nil {
			qr = q
		} else {
			log.Printf("DEBUG: GenerateQR failed: %v", qerr)
		}
	}

	finalHash := strings.TrimSpace(resp.InvoiceHash)
	if finalHash == "" {
		finalHash = apiReq.InvoiceHash
	}
	if finalHash == "" {
		finalHash = hash
	}
	if finalHash != hash {
		return "", fmt.Errorf("accepted invoice hash %q differs from prepared hash %q", finalHash, hash)
	}

	if err := chainStore.CommitAccepted(position, ChainAcceptance{
		InvoiceHash: finalHash,
		InvoiceUUID: apiReq.UUID,
		QR:          qr,
		XMLPath:     xmlPath,
	}); err != nil {
		return "", fmt.Errorf("commit accepted chain position: %w", err)
	}

	if len(resp.Warnings) > 0 {
		for _, w := range resp.Warnings {
			log.Printf("    WARN [%s] %s", w.Code, w.Message)
		}
	}

	if apiReq.InvoiceHash != "" {
		return apiReq.InvoiceHash, nil
	}
	if resp.InvoiceHash != "" {
		return resp.InvoiceHash, nil
	}
	return "", fmt.Errorf("no invoice hash returned for %s %d", table, rowID)
}

func reserveChainPosition(store ChainStateStore, document ChainDocument) (ChainPosition, error) {
	if store == nil {
		return ChainPosition{}, fmt.Errorf("T017 chain state contract unavailable: store is not configured")
	}
	return store.Reserve(document)
}

func applyChainPosition(inv *invoice.Invoice, position ChainPosition) error {
	if position.ICV == 0 {
		return fmt.Errorf("chain position ICV must be positive")
	}
	if position.ICV > uint64(^uint(0)>>1) {
		return fmt.Errorf("chain position ICV %d exceeds invoice counter capacity", position.ICV)
	}
	if strings.TrimSpace(position.PreviousInvoiceHash) == "" {
		return fmt.Errorf("chain position PIH is required")
	}
	inv.UUID = position.InvoiceUUID
	inv.PreviousInvoiceHash = position.PreviousInvoiceHash
	inv.InvoiceCounterValue = int(position.ICV)
	inv.IssueDate = position.IssueAt
	inv.IssueTime = position.IssueAt
	return nil
}

func providerResponseAccepted(resp *models.InvoiceResponse) bool {
	if resp == nil || len(resp.Errors) > 0 {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(resp.Status)) {
	case "REPORTED", "CLEARED", "ACCEPTED", "SUCCESS", "PASS":
		return true
	}
	return resp.ValidationResults != nil &&
		strings.EqualFold(strings.TrimSpace(resp.ValidationResults.Status), "PASS")
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
			Discount:    nullFloat64OrZero(p.Discount),
			TaxCategory: invoice.TaxStandard,
			TaxPercent:  p.VATOrZero(),
		}
		lines = append(lines, li)
	}
	return lines
}

func nullFloat64OrZero(value sql.NullFloat64) float64 {
	if value.Valid {
		return value.Float64
	}
	return 0
}

func discountTaxPercent(products []ProductRow) float64 {
	for _, product := range products {
		if product.VAT.Valid {
			return product.VAT.Float64
		}
	}
	return 15
}
