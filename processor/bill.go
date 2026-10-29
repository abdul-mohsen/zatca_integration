package processor

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/invoice"
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
		xmlDir:       cfg.XMLDir,
		tenantID:     cfg.TenantID,
		merchantID:   cfg.MerchantID,
		sourceBillID: b.ID,
		companyID:    b.CompanyID,
		branchID:     b.BranchID,
		billType:     "invoice",
		documentKind: "bill",
		table:        "bill",
		qrColumn:     "qr_code",
	})
}

// BuildBillInvoice constructs an Invoice (type 388) from a BillRow.
func BuildBillInvoice(cfg *config.Config, b BillRow, products []ProductRow, issueTime time.Time) *invoice.Invoice {
	isStandard := b.BuyerVAT.Valid && b.BuyerVAT.String != ""
	customer := buildCustomer(b, isStandard)

	inv := &invoice.Invoice{
		ID:               fmt.Sprintf("INV-%d", b.ID),
		UUID:             stableInvoiceUUID(cfg, "bill", b.ID),
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
		PreviousInvoiceHash: DefaultPIH,
		InvoiceCounterValue: 1,
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

	if b.SeqNumber.Valid {
		if v, err := strconv.Atoi(b.SeqNumber.String); err == nil {
			inv.InvoiceCounterValue = v
		}
	}

	inv.Lines = buildLines(products)
	return inv
}

// xmlMeta holds the tenant/document context for durable submission and
// filesystem artifact paths.
type xmlMeta struct {
	xmlDir       string // base directory (e.g. /data/zatca-xml)
	tenantID     string // tenant identifier (db_name) - prevents cross-tenant collisions
	merchantID   int64  // numeric master tenant id used by T003 persistence
	sourceBillID int64  // source bill id for zatca_submission.bill_id
	companyID    int64
	branchID     int64
	billType     string // "invoice", "credit", "debit"
	documentKind string // "bill", "credit_note", "debit_note"
	table        string // closed internal legacy table name
	qrColumn     string // DB column name for QR code ("qr_code" for bill, "invoice_qr" for credit_note)
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
