package processor

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/zatca"
)

// ProcessCreditNote processes a single credit note row through the full pipeline.
func ProcessCreditNote(db *sql.DB, svc *zatca.Service, cfg *config.Config, row CreditDebitRow) (string, error) {
	log.Printf("Processing credit note for bill id=%d seq=%s", row.ID, row.SeqNumber.String)

	issueTime := time.Now()
	if row.EffectiveDate.Valid {
		if t, err := ParseFlexible(row.EffectiveDate.String); err == nil {
			issueTime = t
		}
	}

	products, err := LoadProducts(db, row.ID)
	if err != nil {
		return "", fmt.Errorf("load products for bill %d: %w", row.ID, err)
	}

	creditNoteID := row.NoteID.Int64
	inv := BuildCreditNoteInvoice(cfg, row, products, issueTime)
	return signSubmitUpdate(db, svc, inv, creditNoteID, "credit_note", xmlMeta{
		xmlDir:    cfg.XMLDir,
		tenantID:  cfg.TenantID,
		companyID: row.CompanyID,
		branchID:  row.BranchID,
		billType:  "credit",
		qrColumn:  "invoice_qr",
	})
}

// BuildCreditNoteInvoice constructs an Invoice (type 381) from a CreditDebitRow.
func BuildCreditNoteInvoice(cfg *config.Config, row CreditDebitRow, products []ProductRow, issueTime time.Time) *invoice.Invoice {
	isStandard := row.BuyerVAT.Valid && row.BuyerVAT.String != ""
	customer := buildCustomer(row.BillRow, isStandard)

	inv := &invoice.Invoice{
		ID:               fmt.Sprintf("INV-CN-%d", row.ID),
		UUID:             uuid.New().String(),
		IssueDate:        issueTime,
		IssueTime:        issueTime,
		TypeCode:         invoice.TypeCreditNote,
		DocumentCurrency: "SAR",
		TaxCurrency:      "SAR",
		Supplier: invoice.Party{
			RegistrationName:   cfg.Seller.Name,
			RegistrationNameAR: cfg.Seller.NameAR,
			VAT:                cfg.Seller.VAT,
			SchemeID:           "CRN",
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
			if row.PaymentMethod.Valid && row.PaymentMethod.String != "" {
				return row.PaymentMethod.String
			}
			return invoice.PaymentCash
		}(),
		InstructionNote: func() string {
			if row.NoteText.Valid {
				return row.NoteText.String
			}
			return ""
		}(),
		BillingReferenceID: func() string {
			if row.SeqNumber.Valid {
				return row.SeqNumber.String
			}
			return ""
		}(),
	}

	if isStandard {
		inv.SubType = invoice.SubTypeStandard
	} else {
		inv.SubType = invoice.SubTypeSimplified
	}

	if row.SeqNumber.Valid {
		if v, err := strconv.Atoi(row.SeqNumber.String); err == nil {
			inv.InvoiceCounterValue = v
		}
	}

	inv.Lines = buildLines(products)
	return inv
}
