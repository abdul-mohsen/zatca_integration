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

// ProcessDebitNote processes a single debit note row through the full pipeline.
func ProcessDebitNote(db *sql.DB, svc *zatca.Service, cfg *config.Config, row CreditDebitRow) (string, error) {
	sourceID := row.noteSourceID()
	log.Printf("Processing debit note source id=%d original bill id=%d seq=%s", sourceID, row.ID, row.SeqNumber.String)
	if sourceID <= 0 {
		return "", fmt.Errorf("debit note source id is required")
	}

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

	inv := BuildDebitNoteInvoice(cfg, row, products, issueTime)
	return signSubmitUpdate(db, svc, inv, sourceID, "debit_note", xmlMeta{
		xmlDir:    cfg.XMLDir,
		tenantID:  cfg.TenantID,
		companyID: row.CompanyID,
		branchID:  row.BranchID,
		billType:  "debit",
		qrColumn:  "invoice_qr",
	})
}

// ProcessDebitNoteWithChain is the ordered-stream variant. It uses the
// reservation supplied by the combined EGS worker.
func ProcessDebitNoteWithChain(
	db *sql.DB,
	svc *zatca.Service,
	cfg *config.Config,
	row CreditDebitRow,
	chainState DocumentChainState,
) (string, error) {
	hash, _, err := ProcessDebitNoteWithChainOutcome(db, svc, cfg, row, chainState)
	return hash, err
}

// ProcessDebitNoteWithChainOutcome returns the provider outcome alongside the
// hash used by the canonical EGS sequence commit.
func ProcessDebitNoteWithChainOutcome(
	db *sql.DB,
	svc *zatca.Service,
	cfg *config.Config,
	row CreditDebitRow,
	chainState DocumentChainState,
) (string, SubmissionState, error) {
	log.Printf("Processing ordered debit note source id=%d original bill id=%d icv=%d",
		row.noteSourceID(), row.ID, chainState.ICV)
	if err := bindCanonicalNoteIdentity(&chainState.Submission, row, "debit"); err != nil {
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

	sourceID := row.noteSourceID()
	if sourceID == 0 {
		return "", SubmissionTerminal, fmt.Errorf("debit note source id is required")
	}
	inv := BuildDebitNoteInvoiceWithChain(cfg, row, products, issueTime, chainState)
	return signSubmitUpdateOutcome(db, svc, inv, sourceID, "debit_note", xmlMeta{
		xmlDir:     cfg.XMLDir,
		tenantID:   cfg.TenantID,
		companyID:  row.CompanyID,
		branchID:   row.BranchID,
		billType:   "debit",
		qrColumn:   "invoice_qr",
		submission: chainState.Submission,
	})
}

// BuildDebitNoteInvoice constructs an Invoice (type 383) from a CreditDebitRow.
func BuildDebitNoteInvoice(cfg *config.Config, row CreditDebitRow, products []ProductRow, issueTime time.Time) *invoice.Invoice {
	chainState := DocumentChainState{
		DocumentUUID: uuid.New().String(),
		ICV:          1,
	}
	if row.SeqNumber.Valid {
		if value, err := strconv.ParseUint(row.SeqNumber.String, 10, 64); err == nil {
			chainState.ICV = value
		}
	}
	return BuildDebitNoteInvoiceWithChain(cfg, row, products, issueTime, chainState)
}

// BuildDebitNoteInvoiceWithChain constructs a debit note from one EGS
// reservation without generating independent chain state.
func BuildDebitNoteInvoiceWithChain(
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
		ID:               fmt.Sprintf("INV-DN-%d", noteID),
		UUID:             chainState.DocumentUUID,
		IssueDate:        issueTime,
		IssueTime:        issueTime,
		TypeCode:         invoice.TypeDebitNote,
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
