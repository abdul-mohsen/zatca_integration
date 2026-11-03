package queue

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// noteMessage is a valid envelope for one credit or debit note of the fixture. Its invoice UUID is the one that the backend wrote with the
// note, so the envelope and the row agree.
func noteMessage(fx chainFixture, docType DocType, noteID int64, invoiceUUID, request string) Message {
	return Message{
		DocType:            docType,
		ID:                 noteID,
		BranchID:           fx.branchID,
		DBName:             "chain_test",
		TenantKey:          "chain-test-tenant",
		PosID:              fx.posID,
		BillRequestID:      request,
		EventID:            uuid.NewString(),
		EGSSequenceID:      strconv.FormatInt(fx.branchID, 10),
		InvoiceUUID:        invoiceUUID,
		RequestFingerprint: strings.Repeat("c", 64),
	}
}

// insertNote writes a note on a bill of the fixture, as the backend writes it: with its request identity and its own invoice UUID. It
// returns the note's id and its UUID. The table is credit_note or debit_note.
func insertNote(t *testing.T, db *sql.DB, table string, bill int64, amount string) (int64, string) {
	t.Helper()
	invoiceUUID := uuid.NewString()
	id := insertID(t, db, `INSERT INTO `+table+` (bill_id, state, note, amount, lines_total, invoice_uuid, pos_id, bill_request_id, request_fingerprint)
		VALUES (?, 1, 'returned', ?, ?, ?, 'pos-notes', ?, ?)`, bill, amount, amount, invoiceUUID, uuid.NewString(), strings.Repeat("c", 64))
	return id, invoiceUUID
}

// A credit note is committed and recorded next to its bill. It has its own record, keyed by its kind and its id, and its own invoice fields.
// The bill keeps its state and its hash. The bill and the note share one chain: the note takes the next ICV after the bill's.
func TestCreditNoteIsCommittedAndRecordedNextToItsBill(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 2)
	ctx := context.Background()
	chain := NewSQLChain(db)

	bill := fixtureMessage(fx, fx.bills[0], "bill-request")
	billBooking, err := chain.Book(ctx, bill)
	if err != nil {
		t.Fatalf("Book the bill: %v", err)
	}
	billInvoice := signedFor(billBooking, fx.bills[0], "hash-bill")
	if err := chain.Commit(ctx, billBooking, billInvoice); err != nil {
		t.Fatalf("Commit the bill: %v", err)
	}
	if err := chain.Record(ctx, billInvoice, Outcome{State: OutcomeAccepted}); err != nil {
		t.Fatalf("Record the bill: %v", err)
	}

	noteID, noteUUID := insertNote(t, db, "credit_note", fx.bills[0], "11.50")
	note := noteMessage(fx, DocCredit, noteID, noteUUID, "credit-request")
	noteBooking, err := chain.Book(ctx, note)
	if err != nil {
		t.Fatalf("Book the note: %v", err)
	}
	if noteBooking.ICV != 2 || noteBooking.PIH != "hash-bill" {
		t.Fatalf("note booking = ICV %d, PIH %q; want ICV 2 chained from the bill's hash", noteBooking.ICV, noteBooking.PIH)
	}
	noteInvoice := SignedInvoice{
		DocType:   DocCredit,
		SourceID:  noteID,
		BillID:    fx.bills[0],
		BranchID:  fx.branchID,
		UUID:      noteBooking.UUID,
		ICV:       noteBooking.ICV,
		PIH:       noteBooking.PIH,
		Hash:      "hash-note",
		Kind:      InvoiceStandard,
		SignedXML: "<CreditNote>hash-note</CreditNote>",
		QR:        "qr-note",
	}
	if err := chain.Commit(ctx, noteBooking, noteInvoice); err != nil {
		t.Fatalf("Commit the note: %v", err)
	}
	if err := chain.Record(ctx, noteInvoice, Outcome{State: OutcomeAccepted}); err != nil {
		t.Fatalf("Record the note: %v", err)
	}

	stored, err := chain.Stored(ctx, note)
	if err != nil || stored == nil || stored.DocType != DocCredit || stored.SourceID != noteID || stored.BillID != fx.bills[0] ||
		stored.QR != "qr-note" || stored.Outcome != OutcomeAccepted {
		t.Fatalf("Stored(note) = %+v, %v; want the committed note of its bill, with its QR code and ZATCA's answer", stored, err)
	}
	var noteHash, noteQR string
	scanOne(t, db, &noteHash, `SELECT invoice_hash FROM credit_note WHERE id = ?`, noteID)
	scanOne(t, db, &noteQR, `SELECT invoice_qr FROM credit_note WHERE id = ?`, noteID)
	if noteHash != "hash-note" || noteQR != "qr-note" {
		t.Fatalf("note fields = %s, %s; want hash-note and qr-note", noteHash, noteQR)
	}
	var billState int
	var billHash string
	scanOne(t, db, &billState, `SELECT state FROM bill WHERE id = ?`, fx.bills[0])
	scanOne(t, db, &billHash, `SELECT invoice_hash FROM bill WHERE id = ?`, fx.bills[0])
	if billState != 3 || billHash != "hash-bill" {
		t.Fatalf("bill = state %d, hash %s; want it still issued with its own hash", billState, billHash)
	}

	// A bill booked after the note continues the same chain, one number further on.
	next, err := chain.Book(ctx, fixtureMessage(fx, fx.bills[1], "bill-after-note"))
	if err != nil {
		t.Fatalf("Book a bill after the note: %v", err)
	}
	if next.ICV != 3 || next.PIH != "hash-note" {
		t.Fatalf("bill after the note = ICV %d, PIH %q; want ICV 3 chained from the note's hash", next.ICV, next.PIH)
	}
	if err := chain.Release(ctx, next); err != nil {
		t.Fatal(err)
	}
}

// A note has one invoice. A second invoice for it is refused by the database, and the chain does not move.
func TestASecondInvoiceForANoteIsRefusedAndTheChainStays(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 1)
	ctx := context.Background()
	chain := NewSQLChain(db)
	noteID, noteUUID := insertNote(t, db, "debit_note", fx.bills[0], "5.75")

	first := noteMessage(fx, DocDebit, noteID, noteUUID, "debit-first")
	booking, err := chain.Book(ctx, first)
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	invoice := SignedInvoice{DocType: DocDebit, SourceID: noteID, BillID: fx.bills[0], BranchID: fx.branchID, UUID: noteUUID,
		ICV: booking.ICV, PIH: booking.PIH, Hash: "hash-debit-1", Kind: InvoiceStandard, SignedXML: "<DebitNote/>", QR: "qr-1"}
	if err := chain.Commit(ctx, booking, invoice); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	second := noteMessage(fx, DocDebit, noteID, noteUUID, "debit-second")
	next, err := chain.Book(ctx, second)
	if err != nil {
		t.Fatalf("Book the second request: %v", err)
	}
	again := invoice
	again.ICV, again.PIH, again.Hash = next.ICV, next.PIH, "hash-debit-2"
	if err := chain.Commit(ctx, next, again); err == nil {
		t.Fatal("a second invoice for the same note was committed")
	}
	if err := chain.Release(ctx, next); err != nil {
		t.Fatalf("Release: %v", err)
	}

	var lastICV uint64
	var previous string
	scanOne(t, db, &lastICV, `SELECT last_icv FROM branch_zatca_config WHERE branch_id = ?`, fx.branchID)
	scanOne(t, db, &previous, `SELECT previous_invoice_hash FROM branch_zatca_config WHERE branch_id = ?`, fx.branchID)
	if lastICV != 1 || previous != "hash-debit-1" {
		t.Fatalf("chain = ICV %d, previous %q; want the first note's values", lastICV, previous)
	}
}
