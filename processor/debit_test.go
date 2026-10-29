package processor

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/zatca-go/zatca/config"
)

func TestBuildDebitNoteInvoiceUsesDebitSourceAndOriginalBillReference(t *testing.T) {
	cfg := &config.Config{
		Seller: config.SellerInfo{
			Name:    "Seller",
			VAT:     "399999999900003",
			CRN:     "1010010000",
			Country: "SA",
		},
	}
	row := CreditDebitRow{
		BillRow: BillRow{
			ID:        42,
			BranchID:  7,
			Discount:  sql.NullFloat64{Float64: 5, Valid: true},
			BuyerVAT:  sql.NullString{String: "399999999800003", Valid: true},
			SeqNumber: sql.NullString{String: "20260042", Valid: true},
		},
		NoteText: sql.NullString{String: "Additional taxable charge", Valid: true},
		NoteID:   sql.NullInt64{Int64: 77, Valid: true},
	}
	products := []ProductRow{{
		Name:     sql.NullString{String: "Item", Valid: true},
		Price:    sql.NullFloat64{Float64: 20, Valid: true},
		Quantity: sql.NullFloat64{Float64: 2, Valid: true},
		VAT:      sql.NullFloat64{Float64: 15, Valid: true},
		Discount: sql.NullFloat64{Float64: 1.5, Valid: true},
	}}

	inv := BuildDebitNoteInvoice(cfg, row, products, time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))

	if inv.ID != "INV-DN-77" {
		t.Fatalf("debit invoice ID = %q, want source ID INV-DN-77", inv.ID)
	}
	if inv.BillingReferenceID != "INV-42" {
		t.Fatalf("billing reference = %q, want original bill ID INV-42", inv.BillingReferenceID)
	}
	if inv.Discount != 5 {
		t.Fatalf("document discount = %v, want 5", inv.Discount)
	}
	if len(inv.Lines) != 1 || inv.Lines[0].Discount != 1.5 {
		t.Fatalf("line discount = %#v, want 1.5", inv.Lines)
	}
	if inv.InstructionNote != "Additional taxable charge" {
		t.Fatalf("instruction note = %q, want debit reason", inv.InstructionNote)
	}
	if inv.Customer.VAT != "399999999800003" {
		t.Fatalf("customer VAT = %q, want buyer VAT", inv.Customer.VAT)
	}
}

func TestBuildDebitNoteInvoiceXMLContainsOriginalReferenceAndReason(t *testing.T) {
	cfg := &config.Config{
		Seller: config.SellerInfo{
			Name:    "Seller",
			VAT:     "399999999900003",
			CRN:     "1010010000",
			Country: "SA",
		},
	}

	row := CreditDebitRow{
		BillRow: BillRow{ID: 42, BranchID: 7},
		NoteText: sql.NullString{
			String: "Additional taxable charge",
			Valid:  true,
		},
		NoteID: sql.NullInt64{Int64: 77, Valid: true},
	}
	inv := BuildDebitNoteInvoice(cfg, row, []ProductRow{{
		Name:     sql.NullString{String: "Item", Valid: true},
		Price:    sql.NullFloat64{Float64: 10, Valid: true},
		Quantity: sql.NullFloat64{Float64: 1, Valid: true},
		VAT:      sql.NullFloat64{Float64: 15, Valid: true},
	}}, time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))
	if err := applyChainPosition(inv, ChainPosition{
		Document:            ChainDocument{BranchID: 7, Type: DocumentDebit, DocumentID: 77},
		ICV:                 8,
		PreviousInvoiceHash: "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo=",
		InvoiceUUID:         "00000000-0000-0000-0000-000000000077",
		IssueAt:             time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("applyChainPosition: %v", err)
	}

	xml, err := inv.ToXML()
	if err != nil {
		t.Fatalf("ToXML: %v", err)
	}
	for _, want := range []string{
		`<cbc:InvoiceTypeCode name="0200000">383</cbc:InvoiceTypeCode>`,
		"<cbc:ID>INV-42</cbc:ID>",
		"<cbc:InstructionNote>Additional taxable charge</cbc:InstructionNote>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("debit XML missing %q", want)
		}
	}
}

func TestDebitQueriesUseDurableT024SourceContext(t *testing.T) {
	for _, field := range []string{
		"FROM debit_note deb",
		"deb.branch_id",
		"deb.sequence_number",
		"deb.merchant_id",
		"WHERE deb.id = ?",
	} {
		if !strings.Contains(debitNoteByIDQuery, field) {
			t.Errorf("debit source query missing %q", field)
		}
	}
	for _, field := range []string{
		"FROM debit_note deb",
		"WHERE deb.branch_id = ?",
		"deb.state = 1",
	} {
		if !strings.Contains(pendingDebitNotesQuery, field) {
			t.Errorf("debit replay query missing %q", field)
		}
	}
}
