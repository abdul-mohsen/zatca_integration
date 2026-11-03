package processor

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/zatca"
)

// noteDB opens the backend schema named by ZATCA_TEST_DB on the server in ZATCA_TEST_MYSQL_DSN, and skips the test when either is
// unset. The database must have been migrated to goose 00011 by the backend's cmd/migrate up.
func noteDB(t *testing.T) *sql.DB {
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
	return db
}

func noteExec(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	result, err := db.Exec(query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// hexOf is a fingerprint of 64 digits made of one character, which passes the schema's check.
func hexOf(c string) string {
	return strings.Repeat(c, 64)
}

// noteFixture is a company with one branch and a bill of that branch, which ZATCA accepted. The bill has two lines and a total of 40.25.
// It has a credit note of 11.50 on its brake pad, and a debit note of 5.75.
type noteFixture struct {
	db         *sql.DB
	branch     int64
	bill       int64
	brake      int64
	credit     int64
	debit      int64
	creditUUID string
	debitUUID  string
}

func newNoteFixture(t *testing.T) noteFixture {
	t.Helper()
	db := noteDB(t)
	tag := uuid.NewString()
	fx := noteFixture{db: db, creditUUID: uuid.NewString(), debitUUID: uuid.NewString()}
	company := noteExec(t, db, `INSERT INTO company (name, vat_number) VALUES (?, '300000000000003')`, "notes-"+tag)
	fx.branch = noteExec(t, db, `INSERT INTO branches (name, company_id) VALUES (?, ?)`, "branch-"+tag, company)
	store := noteExec(t, db, `INSERT INTO store (name, company_id) VALUES (?, ?)`, "store-"+tag, company)
	fx.bill = noteExec(t, db, `INSERT INTO bill (discount, store_id, merchant_id, maintenance_cost, branch_id, state, total, invoice_uuid, userName)
		VALUES (0, ?, 1, 0, ?, 3, 40.25, ?, 'Buyer')`, store, fx.branch, uuid.NewString())
	fx.brake = noteExec(t, db, `INSERT INTO bill_product (bill_id, name, price, quantity, vat, discount) VALUES (?, 'Brake pad', 10.00, 2.000, 15.00, 0.00)`, fx.bill)
	noteExec(t, db, `INSERT INTO bill_product (bill_id, name, price, quantity, vat, discount) VALUES (?, 'Service', 15.00, 1.000, 15.00, 0.00)`, fx.bill)
	noteExec(t, db, `INSERT INTO zatca_submission (bill_id, branch_id, status, outcome_state, invoice_icv, previous_invoice_hash,
		invoice_uuid, invoice_hash, invoice_kind, signed_xml, document_type, source_id)
		VALUES (?, ?, 2, 'accepted', 1, ?, ?, 'hash-bill', 'standard', '<Invoice/>', 'bill', ?)`,
		fx.bill, fx.branch, queue.GenesisPIH, uuid.NewString(), fx.bill)
	fx.credit = noteExec(t, db, `INSERT INTO credit_note (bill_id, state, note, amount, lines_total, invoice_uuid, pos_id, bill_request_id, request_fingerprint)
		VALUES (?, 1, 'returned', 11.50, 11.50, ?, 'pos-notes', ?, ?)`, fx.bill, fx.creditUUID, uuid.NewString(), hexOf("c"))
	noteExec(t, db, `INSERT INTO credit_note_line (credit_note_id, bill_product_id, name, quantity, price, discount, vat)
		VALUES (?, ?, 'Brake pad', 1.000, 10.00, 0.00, 15.00)`, fx.credit, fx.brake)
	fx.debit = noteExec(t, db, `INSERT INTO debit_note (bill_id, state, note, amount, lines_total, invoice_uuid, pos_id, bill_request_id, request_fingerprint)
		VALUES (?, 1, 'towing', 5.75, 5.75, ?, 'pos-notes', ?, ?)`, fx.bill, fx.debitUUID, uuid.NewString(), hexOf("d"))
	noteExec(t, db, `INSERT INTO debit_note_line (debit_note_id, name, quantity, price, vat) VALUES (?, 'Towing', 1.000, 5.00, 15.00)`, fx.debit)

	t.Cleanup(func() {
		for _, query := range []string{
			`DELETE FROM credit_note WHERE bill_id = ?`,
			`DELETE FROM debit_note WHERE bill_id = ?`,
			`DELETE FROM bill_product WHERE bill_id = ?`,
			`DELETE FROM bill WHERE id = ?`,
		} {
			if _, err := db.Exec(query, fx.bill); err != nil {
				t.Errorf("cleanup %s: %v", query, err)
			}
		}
		_, _ = db.Exec(`DELETE FROM branches WHERE id = ?`, fx.branch)
		_, _ = db.Exec(`DELETE FROM store WHERE id = ?`, store)
		_, _ = db.Exec(`DELETE FROM company WHERE id = ?`, company)
	})
	return fx
}

// pipelineFor is a pipeline whose branch has the verified configuration. Prepare does not call ZATCA, so the service is empty.
func pipelineFor(db *sql.DB) *DocumentPipeline {
	return &DocumentPipeline{DB: db, Service: func(int64) (*zatca.Service, *config.Config, error) {
		return &zatca.Service{}, verifiedConfig(), nil
	}}
}

// The daemon reads a note, its lines, the bill, the bill's stored invoice, and the credits up to the note from the migrated schema. A
// credit note and a debit note are prepared for invoicing, with their lines' totals as the database computed them.
func TestPrepareReadsANoteAndItsBillFromTheSchema(t *testing.T) {
	fx := newNoteFixture(t)
	p := pipelineFor(fx.db)
	credit := queue.Message{DocType: queue.DocCredit, ID: fx.credit, BranchID: fx.branch, InvoiceUUID: fx.creditUUID}
	if _, err := p.Prepare(context.Background(), credit); err != nil {
		t.Fatalf("Prepare the credit note: %v", err)
	}
	lines, err := LoadNoteLines(fx.db, queue.DocCredit, fx.credit)
	if err != nil || len(lines) != 1 || lines[0].TotalIncludingVAT.Float64 != 11.5 {
		t.Fatalf("credit lines = %+v, %v; want one line of 11.50", lines, err)
	}
	through, err := creditedThrough(fx.db, fx.bill, fx.credit)
	if err != nil || through != 11.5 {
		t.Fatalf("credited through the note = %v, %v; want 11.50", through, err)
	}
	original, found, err := loadBillInvoice(fx.db, fx.bill)
	if err != nil || !found || original.Outcome != queue.OutcomeAccepted || original.UUID == "" {
		t.Fatalf("bill invoice = %+v, %v, %v; want the accepted invoice of the bill", original, found, err)
	}

	debit := queue.Message{DocType: queue.DocDebit, ID: fx.debit, BranchID: fx.branch, InvoiceUUID: fx.debitUUID}
	if _, err := p.Prepare(context.Background(), debit); err != nil {
		t.Fatalf("Prepare the debit note: %v", err)
	}
	debitLines, err := LoadNoteLines(fx.db, queue.DocDebit, fx.debit)
	if err != nil || len(debitLines) != 1 || debitLines[0].TotalIncludingVAT.Float64 != 5.75 {
		t.Fatalf("debit lines = %+v, %v; want one line of 5.75", debitLines, err)
	}
}

// A note that the backend would have refused is refused by the daemon too, as an input error.
func TestPrepareRefusesANoteThatCannotBeInvoiced(t *testing.T) {
	fx := newNoteFixture(t)
	p := pipelineFor(fx.db)
	credit := queue.Message{DocType: queue.DocCredit, ID: fx.credit, BranchID: fx.branch, InvoiceUUID: fx.creditUUID}

	if _, err := fx.db.Exec(`UPDATE credit_note SET note = NULL WHERE id = ?`, fx.credit); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Prepare(context.Background(), credit); !errors.Is(err, queue.ErrRejectedInput) {
		t.Fatalf("Prepare without a reason = %v; want ErrRejectedInput", err)
	}
	if _, err := fx.db.Exec(`UPDATE credit_note SET note = 'returned' WHERE id = ?`, fx.credit); err != nil {
		t.Fatal(err)
	}

	// A second credit note of 35.00 takes the credits up to it to 46.50, past the bill total of 40.25. Its own line is well formed, so
	// only the cumulative check refuses it.
	overUUID := uuid.NewString()
	over := noteExec(t, fx.db, `INSERT INTO credit_note (bill_id, state, note, amount, lines_total, invoice_uuid, pos_id, bill_request_id, request_fingerprint)
		VALUES (?, 1, 'second', 35.00, 35.00, ?, 'pos-notes', ?, ?)`, fx.bill, overUUID, uuid.NewString(), hexOf("e"))
	noteExec(t, fx.db, `INSERT INTO credit_note_line (credit_note_id, bill_product_id, name, quantity, price, discount, vat)
		VALUES (?, ?, 'Brake pad', 1.000, 35.00, 0.00, 0.00)`, over, fx.brake)
	overMsg := queue.Message{DocType: queue.DocCredit, ID: over, BranchID: fx.branch, InvoiceUUID: overUUID}
	if _, err := p.Prepare(context.Background(), overMsg); !errors.Is(err, queue.ErrRejectedInput) {
		t.Fatalf("Prepare past the bill total = %v; want ErrRejectedInput", err)
	}
}
