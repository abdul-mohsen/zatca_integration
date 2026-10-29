package processor

import (
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/invoice"
)

func TestBuildBillInvoiceDoesNotUseFixedChainDefaults(t *testing.T) {
	cfg := &config.Config{
		Seller: config.SellerInfo{
			Name:    "Seller",
			VAT:     "399999999900003",
			CRN:     "1010010000",
			Country: "SA",
		},
	}
	bill := BillRow{ID: 7, BranchID: 11}

	inv := BuildBillInvoice(cfg, bill, []ProductRow{{
		Name:     sql.NullString{String: "Item", Valid: true},
		Quantity: sql.NullFloat64{Float64: 1, Valid: true},
		Price:    sql.NullFloat64{Float64: 10, Valid: true},
		VAT:      sql.NullFloat64{Float64: 15, Valid: true},
	}}, time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))

	if inv.InvoiceCounterValue == 1 {
		t.Fatal("bill builder still uses fixed ICV 1")
	}
	if inv.PreviousInvoiceHash != "" {
		t.Fatal("bill builder still supplies a fixed PIH")
	}
}

func TestNoteBuildersDoNotUseFixedChainDefaults(t *testing.T) {
	cfg := &config.Config{
		Seller: config.SellerInfo{
			Name:    "Seller",
			VAT:     "399999999900003",
			CRN:     "1010010000",
			Country: "SA",
		},
	}
	row := CreditDebitRow{
		BillRow: BillRow{ID: 8, BranchID: 11},
		NoteID:  sql.NullInt64{Int64: 9, Valid: true},
	}
	products := []ProductRow{{
		Name:     sql.NullString{String: "Item", Valid: true},
		Quantity: sql.NullFloat64{Float64: 1, Valid: true},
		Price:    sql.NullFloat64{Float64: 10, Valid: true},
		VAT:      sql.NullFloat64{Float64: 15, Valid: true},
	}}
	issued := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		name  string
		build func() *invoice.Invoice
	}{
		{name: "credit", build: func() *invoice.Invoice {
			return BuildCreditNoteInvoice(cfg, row, products, issued)
		}},
		{name: "debit", build: func() *invoice.Invoice {
			return BuildDebitNoteInvoice(cfg, row, products, issued)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inv := test.build()
			if inv.InvoiceCounterValue == 1 {
				t.Fatal("note builder still uses fixed ICV 1")
			}
			if inv.PreviousInvoiceHash != "" {
				t.Fatal("note builder still supplies a fixed PIH")
			}
		})
	}
}

func TestMemoryChainStateStoreSharesPositionsAcrossDocumentTypes(t *testing.T) {
	store := NewMemoryChainStateStore(map[int64]ChainState{
		11: {BranchID: 11, PreviousInvoiceHash: "branch-11-pih", LastICV: 40},
		12: {BranchID: 12, PreviousInvoiceHash: "branch-12-pih", LastICV: 7},
	})
	issued := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	var positions []ChainPosition
	for _, docType := range []DocumentType{DocumentBill, DocumentCredit, DocumentDebit} {
		position, err := store.Reserve(ChainDocument{
			BranchID:        11,
			Type:            docType,
			DocumentID:      int64(len(positions) + 1),
			ProposedUUID:    strings.Repeat("a", 8) + string(docType),
			ProposedIssueAt: issued,
		})
		if err != nil {
			t.Fatalf("Reserve(%s): %v", docType, err)
		}
		positions = append(positions, position)
	}

	for i, position := range positions {
		want := uint64(41 + i)
		if position.ICV != want {
			t.Errorf("%s ICV = %d, want %d", position.Document.Type, position.ICV, want)
		}
		if position.PreviousInvoiceHash != "branch-11-pih" {
			t.Errorf("%s PIH = %q, want branch-11-pih", position.Document.Type, position.PreviousInvoiceHash)
		}
	}

	other, err := store.Reserve(ChainDocument{
		BranchID:        12,
		Type:            DocumentBill,
		DocumentID:      1,
		ProposedUUID:    "branch-12-doc",
		ProposedIssueAt: issued,
	})
	if err != nil {
		t.Fatalf("Reserve(other branch): %v", err)
	}
	if other.ICV != 8 || other.PreviousInvoiceHash != "branch-12-pih" {
		t.Fatalf("other branch position = %#v", other)
	}
}

func TestMemoryChainStateStoreReservationAndAcceptedCommitAreRetrySafe(t *testing.T) {
	store := NewMemoryChainStateStore(map[int64]ChainState{
		11: {BranchID: 11, PreviousInvoiceHash: "previous", LastICV: 9},
	})
	doc := ChainDocument{
		BranchID:        11,
		Type:            DocumentBill,
		DocumentID:      22,
		ProposedUUID:    "stable-uuid",
		ProposedIssueAt: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
	}

	first, err := store.Reserve(doc)
	if err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	if err := store.SavePrepared(first, "<signed/>", "prepared-hash"); err != nil {
		t.Fatalf("SavePrepared: %v", err)
	}

	retry, err := store.Reserve(doc)
	if err != nil {
		t.Fatalf("retry Reserve: %v", err)
	}
	if retry.ICV != first.ICV || retry.InvoiceUUID != first.InvoiceUUID ||
		retry.SignedXML != "<signed/>" || retry.InvoiceHash != "prepared-hash" {
		t.Fatalf("retry did not reuse prepared position: first=%#v retry=%#v", first, retry)
	}

	accepted := ChainAcceptance{InvoiceHash: "accepted-hash", InvoiceUUID: first.InvoiceUUID, QR: "qr"}
	if err := store.CommitAccepted(first, accepted); err != nil {
		t.Fatalf("CommitAccepted: %v", err)
	}
	if err := store.CommitAccepted(retry, accepted); err != nil {
		t.Fatalf("duplicate CommitAccepted: %v", err)
	}

	state, ok := store.State(11)
	if !ok {
		t.Fatal("branch state missing")
	}
	if state.LastICV != 10 || state.PreviousInvoiceHash != "accepted-hash" {
		t.Fatalf("accepted state = %#v", state)
	}
}

func TestMemoryChainStateStoreAllocatesUniquePositionsConcurrently(t *testing.T) {
	store := NewMemoryChainStateStore(map[int64]ChainState{
		11: {BranchID: 11, PreviousInvoiceHash: "previous", LastICV: 100},
	})

	const count = 20
	positions := make(chan ChainPosition, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			position, err := store.Reserve(ChainDocument{
				BranchID:     11,
				Type:         DocumentType("bill"),
				DocumentID:   int64(id + 1),
				ProposedUUID: string(rune(id + 1000)),
			})
			if err != nil {
				errs <- err
				return
			}
			positions <- position
		}(i)
	}
	wg.Wait()
	close(positions)
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
	seen := map[uint64]bool{}
	for position := range positions {
		if seen[position.ICV] {
			t.Fatalf("duplicate concurrent ICV %d", position.ICV)
		}
		seen[position.ICV] = true
	}
	if len(seen) != count {
		t.Fatalf("got %d positions, want %d", len(seen), count)
	}
}
