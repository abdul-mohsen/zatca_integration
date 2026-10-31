package processor

import (
	"database/sql"
	"testing"
	"time"

	"github.com/zatca-go/zatca/config"
)

func TestOrderedInvoiceBuildersKeepReservedChainState(t *testing.T) {
	cfg := &config.Config{
		Seller: config.SellerInfo{
			Name:    "Seller",
			VAT:     "310000000000003",
			CRN:     "1010010000",
			Country: "SA",
		},
	}
	issueTime := time.Unix(100, 0)
	previousHash := "previous-hash"
	state := DocumentChainState{
		DocumentUUID:        "00000000-0000-4000-8000-000000000001",
		ICV:                 42,
		PreviousInvoiceHash: &previousHash,
	}
	bill := BillRow{ID: 1}
	products := []ProductRow{{Name: sql.NullString{String: "item", Valid: true}}}

	invoice := BuildBillInvoiceWithChain(cfg, bill, products, issueTime, state)
	if invoice.UUID != state.DocumentUUID {
		t.Fatalf("bill UUID = %q, want %q", invoice.UUID, state.DocumentUUID)
	}
	if invoice.InvoiceCounterValue != int(state.ICV) {
		t.Fatalf("bill ICV = %d, want %d", invoice.InvoiceCounterValue, state.ICV)
	}
	if invoice.PreviousInvoiceHash != previousHash {
		t.Fatalf("bill PIH = %q, want %q", invoice.PreviousInvoiceHash, previousHash)
	}

	credit := BuildCreditNoteInvoiceWithChain(cfg, CreditDebitRow{
		BillRow: bill,
		NoteID:  sql.NullInt64{Int64: 2, Valid: true},
	}, products, issueTime, state)
	if credit.UUID != state.DocumentUUID ||
		credit.InvoiceCounterValue != int(state.ICV) ||
		credit.PreviousInvoiceHash != previousHash {
		t.Fatalf("credit chain state = uuid %q icv %d pih %q", credit.UUID, credit.InvoiceCounterValue, credit.PreviousInvoiceHash)
	}

	debit := BuildDebitNoteInvoiceWithChain(cfg, CreditDebitRow{
		BillRow: bill,
		NoteID:  sql.NullInt64{Int64: 3, Valid: true},
	}, products, issueTime, state)
	if debit.UUID != state.DocumentUUID ||
		debit.InvoiceCounterValue != int(state.ICV) ||
		debit.PreviousInvoiceHash != previousHash {
		t.Fatalf("debit chain state = uuid %q icv %d pih %q", debit.UUID, debit.InvoiceCounterValue, debit.PreviousInvoiceHash)
	}
}

func TestOrderedInvoiceBuildersUseGenesisPIHForFirstReservation(t *testing.T) {
	cfg := &config.Config{
		Seller: config.SellerInfo{
			Name:    "Seller",
			VAT:     "310000000000003",
			CRN:     "1010010000",
			Country: "SA",
		},
	}
	invoice := BuildBillInvoiceWithChain(
		cfg,
		BillRow{ID: 1},
		[]ProductRow{{Name: sql.NullString{String: "item", Valid: true}}},
		time.Unix(100, 0),
		DocumentChainState{
			DocumentUUID: "00000000-0000-4000-8000-000000000002",
			ICV:          1,
		},
	)
	if invoice.PreviousInvoiceHash != DefaultPIH {
		t.Fatalf("first invoice PIH = %q, want genesis hash", invoice.PreviousInvoiceHash)
	}
}
