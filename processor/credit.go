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
	sourceID := row.noteSourceID()
	log.Printf("Processing credit note source id=%d original bill id=%d seq=%s", sourceID, row.ID, row.SeqNumber.String)

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
	if sourceID > 0 {
		creditNoteID = sourceID
	}
	if creditNoteID <= 0 {
		return "", fmt.Errorf("credit note source id is required")
	}
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

// ProcessCreditNoteWithChain is the ordered-stream variant. It uses the
// reservation supplied by the combined EGS worker.
func ProcessCreditNoteWithChain(
	db *sql.DB,
	svc *zatca.Service,
	cfg *config.Config,
	row CreditDebitRow,
	chainState DocumentChainState,
) (string, error) {
	hash, _, err := ProcessCreditNoteWithChainOutcome(db, svc, cfg, row, chainState)
	return hash, err
}

// ProcessCreditNoteWithChainOutcome returns the provider outcome alongside the
// hash used by the canonical EGS sequence commit.
func ProcessCreditNoteWithChainOutcome(
	db *sql.DB,
	svc *zatca.Service,
	cfg *config.Config,
	row CreditDebitRow,
	chainState DocumentChainState,
) (string, SubmissionState, error) {
	log.Printf("Processing ordered credit note source id=%d original bill id=%d icv=%d",
		row.noteSourceID(), row.ID, chainState.ICV)
	if err := bindCanonicalNoteIdentity(&chainState.Submission, row, "credit"); err != nil {
		return "", SubmissionTerminal, err
	}

	issueTime := time.Now()
	if row.EffectiveDate.Valid {
		if t, err := ParseFlexible(row.EffectiveDate.String); err == nil {
			issueTime = t
		}
	}
	products, err := LoadProducts(db, row.ID)
	if err != nil {
		return "", SubmissionTerminal, fmt.Errorf("load products for bill %d: %w", row.ID, err)
	}

	creditNoteID := row.noteSourceID()
	if creditNoteID == 0 {
		return "", SubmissionTerminal, fmt.Errorf("credit note source id is required")
	}
	inv := BuildCreditNoteInvoiceWithChain(cfg, row, products, issueTime, chainState)
	return signSubmitUpdateOutcome(db, svc, inv, creditNoteID, "credit_note", xmlMeta{
		xmlDir:     cfg.XMLDir,
		tenantID:   cfg.TenantID,
		companyID:  row.CompanyID,
		branchID:   row.BranchID,
		billType:   "credit",
		qrColumn:   "invoice_qr",
		submission: chainState.Submission,
	})
}

// BuildCreditNoteInvoice constructs an Invoice (type 381) from a CreditDebitRow.
func BuildCreditNoteInvoice(cfg *config.Config, row CreditDebitRow, products []ProductRow, issueTime time.Time) *invoice.Invoice {
	chainState := DocumentChainState{
		DocumentUUID: uuid.New().String(),
		ICV:          1,
	}
	if row.SeqNumber.Valid {
		if value, err := strconv.ParseUint(row.SeqNumber.String, 10, 64); err == nil {
			chainState.ICV = value
		}
	}
	return BuildCreditNoteInvoiceWithChain(cfg, row, products, issueTime, chainState)
}

// BuildCreditNoteInvoiceWithChain constructs a credit note from one EGS
// reservation without generating independent chain state.
func BuildCreditNoteInvoiceWithChain(
	cfg *config.Config,
	row CreditDebitRow,
	products []ProductRow,
	issueTime time.Time,
	chainState DocumentChainState,
) *invoice.Invoice {
	isStandard := row.BuyerVAT.Valid && row.BuyerVAT.String != ""
	customer := buildCustomer(row.BillRow, isStandard)
	if chainState.DocumentUUID == "" {
		chainState.DocumentUUID = uuid.New().String()
	}
	if chainState.ICV == 0 {
		chainState.ICV = 1
	}
	noteID := row.noteSourceID()
	reason := ""
	if row.NoteText.Valid {
		reason = row.NoteText.String
	}

	inv := &invoice.Invoice{
		ID:               fmt.Sprintf("INV-CN-%d", noteID),
		UUID:             chainState.DocumentUUID,
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
		PreviousInvoiceHash: chainState.effectivePIH(),
		InvoiceCounterValue: int(chainState.ICV),
		PaymentMeans: func() string {
			if row.PaymentMethod.Valid && row.PaymentMethod.String != "" {
				return row.PaymentMethod.String
			}
			return invoice.PaymentCash
		}(),
		InstructionNote: func() string {
			return reason
		}(),
		Note:                 reason,
		BillingReferenceID:   row.originalReferenceID(),
		BillingReferenceUUID: row.originalReferenceUUID(),
	}

	if isStandard {
		inv.SubType = invoice.SubTypeStandard
	} else {
		inv.SubType = invoice.SubTypeSimplified
	}

	if chainState.ICV == 0 && row.SeqNumber.Valid {
		if v, err := strconv.Atoi(row.SeqNumber.String); err == nil {
			inv.InvoiceCounterValue = v
		}
	}

	inv.Lines = buildLines(products)
	return inv
}
