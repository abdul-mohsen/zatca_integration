package queue

import (
	"context"
	"testing"
)

// A recorded answer is final. Recording again never changes the answer, the stored hash, or the bill.
func TestRecordNeverChangesAnAnswerOrTheHash(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 1)
	ctx := context.Background()
	chain := NewSQLChain(db)
	msg := fixtureMessage(fx, fx.bills[0], "answered")

	booking, err := chain.Book(ctx, msg)
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	invoice := signedFor(booking, fx.bills[0], "hash-answered")
	if err := chain.Commit(ctx, booking, invoice); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := chain.Record(ctx, invoice, Outcome{State: OutcomeAccepted}); err != nil {
		t.Fatalf("Record accepted: %v", err)
	}
	if err := chain.Record(ctx, invoice, Outcome{State: OutcomeRejected, ProviderCode: "400", Message: "later"}); err != nil {
		t.Fatalf("Record a second answer: %v", err)
	}

	stored, err := chain.Stored(ctx, msg)
	if err != nil || stored == nil {
		t.Fatalf("Stored = %v, %v; want the committed invoice", stored, err)
	}
	if stored.Outcome != OutcomeAccepted || stored.Hash != "hash-answered" {
		t.Fatalf("stored = outcome %q, hash %q; want the first answer and the committed hash", stored.Outcome, stored.Hash)
	}
	var billState int
	scanOne(t, db, &billState, `SELECT state FROM bill WHERE id = ?`, fx.bills[0])
	if billState != 3 {
		t.Fatalf("bill state = %d; want 3 after the accepted answer", billState)
	}
	var answers int
	scanOne(t, db, &answers, `SELECT COUNT(*) FROM zatca_submission_outcome o JOIN zatca_submission s ON s.id = o.submission_id WHERE s.bill_id = ?`, fx.bills[0])
	if answers != 1 {
		t.Fatalf("recorded answers = %d; want 1", answers)
	}
}

// A bill has one invoice. A second invoice for it is refused by the database, so the first hash cannot be replaced, and the
// chain does not move.
func TestASecondInvoiceForABillIsRefusedAndTheChainStays(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 1)
	ctx := context.Background()
	chain := NewSQLChain(db)

	first := fixtureMessage(fx, fx.bills[0], "first")
	booking, err := chain.Book(ctx, first)
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	if err := chain.Commit(ctx, booking, signedFor(booking, fx.bills[0], "hash-1")); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	second := fixtureMessage(fx, fx.bills[0], "second")
	next, err := chain.Book(ctx, second)
	if err != nil {
		t.Fatalf("Book the second request: %v", err)
	}
	if next.ICV != 2 || next.PIH != "hash-1" {
		t.Fatalf("second booking = ICV %d, PIH %q; want 2 from hash-1", next.ICV, next.PIH)
	}
	if err := chain.Commit(ctx, next, signedFor(next, fx.bills[0], "hash-2")); err == nil {
		t.Fatal("a second invoice for the same bill was committed")
	}
	if err := chain.Release(ctx, next); err != nil {
		t.Fatalf("Release: %v", err)
	}

	var lastICV uint64
	var previous, hash string
	scanOne(t, db, &lastICV, `SELECT last_icv FROM branch_zatca_config WHERE branch_id = ?`, fx.branchID)
	scanOne(t, db, &previous, `SELECT previous_invoice_hash FROM branch_zatca_config WHERE branch_id = ?`, fx.branchID)
	scanOne(t, db, &hash, `SELECT invoice_hash FROM bill WHERE id = ?`, fx.bills[0])
	if lastICV != 1 || previous != "hash-1" || hash != "hash-1" {
		t.Fatalf("chain = ICV %d, previous %q, bill hash %q; want the first invoice's values", lastICV, previous, hash)
	}
}

// A released claim is taken again at once. The lease exists for a crash, not for a failure that was handled.
func TestReleasedClaimIsTakenAgainAtOnce(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 1)
	ctx := context.Background()
	claims := NewSQLClaimStore(db)
	first := fixtureMessage(fx, fx.bills[0], "released-claim")
	replay := fixtureMessage(fx, fx.bills[0], "released-claim")

	if decision, err := claims.Claim(ctx, first); err != nil || decision != ClaimProcess {
		t.Fatalf("first Claim = %v, %v; want ClaimProcess", decision, err)
	}
	if decision, err := claims.Claim(ctx, replay); err != nil || decision != ClaimInFlight {
		t.Fatalf("Claim while leased = %v, %v; want ClaimInFlight", decision, err)
	}
	if err := claims.Release(ctx, first); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if decision, err := claims.Claim(ctx, replay); err != nil || decision != ClaimProcess {
		t.Fatalf("Claim after Release = %v, %v; want ClaimProcess at once", decision, err)
	}
}
