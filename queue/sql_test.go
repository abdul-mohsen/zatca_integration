package queue

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// testDB opens the database named by ZATCA_TEST_DB on the server in ZATCA_TEST_MYSQL_DSN, and skips the test when either is
// unset. The database must hold the backend's schema, built with ifritah-go's cmd/migrate up. These tests use the real
// tables, not a copy of them.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("ZATCA_TEST_MYSQL_DSN")
	name := os.Getenv("ZATCA_TEST_DB")
	if dsn == "" || name == "" {
		t.Skip("ZATCA_TEST_MYSQL_DSN and ZATCA_TEST_DB are not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE()
		  AND table_name IN ('branch_zatca_config', 'zatca_submission', 'zatca_consumer_claim')
	`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 3 {
		t.Fatalf("%s lacks the backend schema: run ifritah-go's cmd/migrate up on it first", name)
	}
	return db
}

// chainFixture is one company, branch, and set of bills. Its rows are removed when the test ends.
type chainFixture struct {
	db       *sql.DB
	branchID int64
	posID    string
	bills    []int64
}

func newChainFixture(t *testing.T, db *sql.DB, bills int) chainFixture {
	t.Helper()
	ctx := context.Background()
	tag := uuid.NewString()
	fx := chainFixture{db: db, posID: "pos-" + tag}

	companyID := insertID(t, db, `INSERT INTO company (name, vat_number) VALUES (?, '300000000000003')`, "chain-"+tag)
	fx.branchID = insertID(t, db, `INSERT INTO branches (name, company_id) VALUES (?, ?)`, "branch-"+tag, companyID)
	mustExec(t, db, `INSERT INTO branch_zatca_config (branch_id) VALUES (?)`, fx.branchID)
	for i := 0; i < bills; i++ {
		fx.bills = append(fx.bills, insertID(t, db,
			`INSERT INTO bill (discount, store_id, merchant_id, maintenance_cost, branch_id) VALUES (0, 1, 1, 0, ?)`, fx.branchID))
	}

	t.Cleanup(func() {
		for _, stmt := range []struct {
			query string
			arg   any
		}{
			{`DELETE FROM zatca_consumer_claim WHERE pos_id = ?`, fx.posID},
			{`DELETE FROM credit_note WHERE bill_id IN (SELECT id FROM bill WHERE branch_id = ?)`, fx.branchID},
			{`DELETE FROM debit_note WHERE bill_id IN (SELECT id FROM bill WHERE branch_id = ?)`, fx.branchID},
			{`DELETE FROM bill WHERE branch_id = ?`, fx.branchID},
			{`DELETE FROM branches WHERE id = ?`, fx.branchID},
			{`DELETE FROM company WHERE id = ?`, companyID},
		} {
			if _, err := db.ExecContext(ctx, stmt.query, stmt.arg); err != nil {
				t.Errorf("cleanup %s: %v", stmt.query, err)
			}
		}
	})
	return fx
}

func insertID(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	result, err := db.ExecContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// fixtureMessage is a valid envelope for one bill of the fixture, with its own request and event.
func fixtureMessage(fx chainFixture, bill int64, request string) Message {
	return Message{
		DocType:            DocBill,
		ID:                 bill,
		BranchID:           fx.branchID,
		DBName:             "chain_test",
		TenantKey:          "chain-test-tenant",
		PosID:              fx.posID,
		BillRequestID:      request,
		EventID:            uuid.NewString(),
		EGSSequenceID:      strconv.FormatInt(fx.branchID, 10),
		InvoiceUUID:        uuid.NewString(),
		RequestFingerprint: strings.Repeat("a", 64),
	}
}

// signedFor is the invoice a builder would produce under a booking, for a bill.
func signedFor(b Booking, bill int64, hash string) SignedInvoice {
	return SignedInvoice{
		DocType:   DocBill,
		SourceID:  bill,
		BillID:    bill,
		BranchID:  b.BranchID,
		UUID:      b.UUID,
		ICV:       b.ICV,
		PIH:       b.PIH,
		Hash:      hash,
		Kind:      InvoiceStandard,
		SignedXML: "<Invoice>" + hash + "</Invoice>",
		QR:        "qr-" + hash,
	}
}

func scanOne(t *testing.T, db *sql.DB, dest any, query string, args ...any) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(dest); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func TestBookingHoldsTheChainUntilItIsReleased(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 2)
	chain := NewSQLChain(db)
	first := fixtureMessage(fx, fx.bills[0], "request-1")
	second := fixtureMessage(fx, fx.bills[1], "request-2")

	booking, err := chain.Book(context.Background(), first)
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	if booking.ICV != 1 || booking.PIH != GenesisPIH {
		t.Fatalf("first booking = ICV %d, PIH %q; want ICV 1 from the genesis hash", booking.ICV, booking.PIH)
	}
	if _, err := chain.Book(context.Background(), second); !errors.Is(err, ErrChainBusy) {
		t.Fatalf("a second request booked while the first holds the chain: %v", err)
	}
	if err := chain.Release(context.Background(), booking); err != nil {
		t.Fatalf("Release: %v", err)
	}
	next, err := chain.Book(context.Background(), second)
	if err != nil {
		t.Fatalf("Book after Release: %v", err)
	}
	if next.ICV != 1 {
		t.Fatalf("ICV after a release = %d; want 1, because nothing was committed", next.ICV)
	}
	if err := chain.Release(context.Background(), next); err != nil {
		t.Fatal(err)
	}
}

func TestBookingOfACrashedRequestIsTakenOverAfterItsLease(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 2)
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	chain := &SQLChain{DB: db, LeaseDuration: 5 * time.Minute, Now: func() time.Time { return start }}
	first := fixtureMessage(fx, fx.bills[0], "crashed")
	second := fixtureMessage(fx, fx.bills[1], "waiting")

	if _, err := chain.Book(context.Background(), first); err != nil {
		t.Fatalf("Book: %v", err)
	}
	chain.Now = func() time.Time { return start.Add(time.Minute) }
	if _, err := chain.Book(context.Background(), second); !errors.Is(err, ErrChainBusy) {
		t.Fatalf("Book inside the lease = %v; want ErrChainBusy", err)
	}
	chain.Now = func() time.Time { return start.Add(6 * time.Minute) }
	booking, err := chain.Book(context.Background(), second)
	if err != nil {
		t.Fatalf("Book after the lease: %v", err)
	}
	if booking.ICV != 1 {
		t.Fatalf("ICV after a crash = %d; want 1, the crashed booking committed nothing", booking.ICV)
	}
}

func TestCommitStoresTheInvoiceAndAdvancesTheChain(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 2)
	ctx := context.Background()
	chain := NewSQLChain(db)
	first := fixtureMessage(fx, fx.bills[0], "request-1")

	booking, err := chain.Book(ctx, first)
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	invoice := signedFor(booking, fx.bills[0], "hash-1")
	if err := chain.Commit(ctx, booking, invoice); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	stored, err := chain.Stored(ctx, first)
	if err != nil || stored == nil {
		t.Fatalf("Stored = %v, %v; want the committed invoice", stored, err)
	}
	if *stored != invoice {
		t.Fatalf("stored = %+v; want %+v", *stored, invoice)
	}
	var lastICV uint64
	var previous string
	scanOne(t, db, &lastICV, `SELECT last_icv FROM branch_zatca_config WHERE branch_id = ?`, fx.branchID)
	scanOne(t, db, &previous, `SELECT previous_invoice_hash FROM branch_zatca_config WHERE branch_id = ?`, fx.branchID)
	if lastICV != 1 || previous != "hash-1" {
		t.Fatalf("chain = ICV %d, previous hash %q; want 1 and hash-1", lastICV, previous)
	}
	var reserved sql.NullString
	scanOne(t, db, &reserved, `SELECT icv_reservation_key FROM branch_zatca_config WHERE branch_id = ?`, fx.branchID)
	if reserved.Valid {
		t.Fatal("the booking was not dropped by the commit")
	}
	var uuidOnBill, hashOnBill string
	scanOne(t, db, &uuidOnBill, `SELECT invoice_uuid FROM bill WHERE id = ?`, fx.bills[0])
	scanOne(t, db, &hashOnBill, `SELECT invoice_hash FROM bill WHERE id = ?`, fx.bills[0])
	if uuidOnBill != invoice.UUID || hashOnBill != "hash-1" {
		t.Fatalf("bill fields = %s, %s; want the committed UUID and hash", uuidOnBill, hashOnBill)
	}

	second := fixtureMessage(fx, fx.bills[1], "request-2")
	next, err := chain.Book(ctx, second)
	if err != nil {
		t.Fatalf("Book the second bill: %v", err)
	}
	if next.ICV != 2 || next.PIH != "hash-1" {
		t.Fatalf("second booking = ICV %d, PIH %q; want 2 chained from hash-1", next.ICV, next.PIH)
	}
	if err := chain.Commit(ctx, booking, invoice); !errors.Is(err, ErrChainConflict) {
		t.Fatalf("a stale booking committed: %v", err)
	}
	if err := chain.Release(ctx, next); err != nil {
		t.Fatal(err)
	}
}

func TestOutcomeIsRecordedAndOnlyAcceptanceIssuesTheBill(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 2)
	ctx := context.Background()
	chain := NewSQLChain(db)

	accepted := fixtureMessage(fx, fx.bills[0], "accepted")
	booking, err := chain.Book(ctx, accepted)
	if err != nil {
		t.Fatal(err)
	}
	invoice := signedFor(booking, fx.bills[0], "hash-accepted")
	if err := chain.Commit(ctx, booking, invoice); err != nil {
		t.Fatal(err)
	}
	if err := chain.Record(ctx, invoice, Outcome{State: OutcomeAccepted}); err != nil {
		t.Fatalf("Record accepted: %v", err)
	}
	var status int
	var state string
	var issued int
	scanOne(t, db, &status, `SELECT status FROM zatca_submission WHERE bill_id = ?`, fx.bills[0])
	scanOne(t, db, &state, `SELECT outcome_state FROM zatca_submission WHERE bill_id = ?`, fx.bills[0])
	scanOne(t, db, &issued, `SELECT state FROM bill WHERE id = ?`, fx.bills[0])
	if status != 2 || state != OutcomeAccepted || issued != 3 {
		t.Fatalf("accepted: status %d, outcome %s, bill state %d; want 2, accepted, 3", status, state, issued)
	}

	rejected := fixtureMessage(fx, fx.bills[1], "rejected")
	booking, err = chain.Book(ctx, rejected)
	if err != nil {
		t.Fatal(err)
	}
	invoice = signedFor(booking, fx.bills[1], "hash-rejected")
	if err := chain.Commit(ctx, booking, invoice); err != nil {
		t.Fatal(err)
	}
	if err := chain.Record(ctx, invoice, Outcome{State: OutcomeRejected, ProviderCode: "400", Message: "NOT_VALID"}); err != nil {
		t.Fatalf("Record rejected: %v", err)
	}
	var rejection string
	scanOne(t, db, &rejection, `SELECT rejection_msg FROM zatca_submission WHERE bill_id = ?`, fx.bills[1])
	scanOne(t, db, &issued, `SELECT state FROM bill WHERE id = ?`, fx.bills[1])
	if rejection != "NOT_VALID" || issued != 0 {
		t.Fatalf("rejected: message %q, bill state %d; want NOT_VALID and the bill not issued", rejection, issued)
	}
	var outcomes int
	scanOne(t, db, &outcomes, `SELECT COUNT(*) FROM zatca_submission_outcome o JOIN zatca_submission s ON s.id = o.submission_id WHERE s.branch_id = ?`, fx.branchID)
	if outcomes != 2 {
		t.Fatalf("outcome log rows = %d; want one per recorded outcome", outcomes)
	}
}

func TestClaimIsProcessedOnceAndAFingerprintChangeConflicts(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 1)
	ctx := context.Background()
	claims := NewSQLClaimStore(db)
	first := fixtureMessage(fx, fx.bills[0], "claimed")

	if decision, err := claims.Claim(ctx, first); err != nil || decision != ClaimProcess {
		t.Fatalf("first Claim = %v, %v; want ClaimProcess", decision, err)
	}
	replay := first
	replay.EventID = uuid.NewString()
	if decision, err := claims.Claim(ctx, replay); err != nil || decision != ClaimInFlight {
		t.Fatalf("Claim while leased = %v, %v; want ClaimInFlight", decision, err)
	}
	if err := claims.Complete(ctx, first); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if decision, err := claims.Claim(ctx, replay); err != nil || decision != ClaimDuplicate {
		t.Fatalf("Claim after Complete = %v, %v; want ClaimDuplicate", decision, err)
	}

	changed := replay
	changed.RequestFingerprint = strings.Repeat("b", 64)
	if decision, err := claims.Claim(ctx, changed); err != nil || decision != ClaimConflict {
		t.Fatalf("Claim with another fingerprint = %v, %v; want ClaimConflict", decision, err)
	}
	reusedEvent := fixtureMessage(fx, fx.bills[0], "other-request")
	reusedEvent.EventID = first.EventID
	if decision, err := claims.Claim(ctx, reusedEvent); err != nil || decision != ClaimConflict {
		t.Fatalf("Claim with another request's event = %v, %v; want ClaimConflict", decision, err)
	}
}

func TestClaimOfACrashedDeliveryIsReclaimedAfterItsLease(t *testing.T) {
	db := testDB(t)
	fx := newChainFixture(t, db, 1)
	ctx := context.Background()
	claims := &SQLClaimStore{DB: db, LeaseDuration: time.Second}
	msg := fixtureMessage(fx, fx.bills[0], "crashed-claim")

	if decision, err := claims.Claim(ctx, msg); err != nil || decision != ClaimProcess {
		t.Fatalf("first Claim = %v, %v; want ClaimProcess", decision, err)
	}
	time.Sleep(2500 * time.Millisecond)
	if decision, err := claims.Claim(ctx, msg); err != nil || decision != ClaimProcess {
		t.Fatalf("Claim after the lease = %v, %v; want ClaimProcess", decision, err)
	}
}
