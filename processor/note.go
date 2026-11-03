package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/zatca"
)

// noteTolerance is the difference that a money comparison allows. It is half a halala, below the smallest amount a note stores.
const noteTolerance = 0.005

// NoteRow is a credit or debit note, as the daemon reads it to invoice it. Its lines are read with LoadNoteLines.
type NoteRow struct {
	ID          int64
	BillID      int64
	State       sql.NullInt64
	Reason      sql.NullString
	Amount      float64
	LinesTotal  float64
	InvoiceUUID sql.NullString
	CreatedAt   sql.NullString
}

// billInvoice is the stored invoice of a bill, which a note refers to: its UUID, and the answer that ZATCA recorded for it.
type billInvoice struct {
	UUID    string
	Outcome string
}

const (
	creditNoteSQL = `SELECT id, bill_id, state, note, amount, lines_total, invoice_uuid, created_at FROM credit_note WHERE id = ?`
	debitNoteSQL  = `SELECT id, bill_id, state, note, amount, lines_total, invoice_uuid, created_at FROM debit_note WHERE id = ?`
)

// LoadNote reads a credit or debit note by its id. A note that does not exist gives sql.ErrNoRows.
func LoadNote(db *sql.DB, docType queue.DocType, noteID int64) (NoteRow, error) {
	query := creditNoteSQL
	if docType == queue.DocDebit {
		query = debitNoteSQL
	}
	var note NoteRow
	err := db.QueryRow(query, noteID).Scan(
		&note.ID, &note.BillID, &note.State, &note.Reason, &note.Amount, &note.LinesTotal, &note.InvoiceUUID, &note.CreatedAt,
	)
	if err != nil {
		return NoteRow{}, fmt.Errorf("query %s %d: %w", docType, noteID, err)
	}
	return note, nil
}

// LoadNoteLines loads the lines of a note in their order. A credit line takes its product from the bill line it credits. Each line
// carries the totals that the database computed for it.
func LoadNoteLines(db *sql.DB, docType queue.DocType, noteID int64) ([]ProductRow, error) {
	query := `SELECT bp.product_id, cl.name, cl.price, cl.quantity, cl.vat, cl.discount,
	                 cl.total_before_vat, cl.vat_total, cl.total_including_vat
	          FROM credit_note_line cl
	          LEFT JOIN bill_product bp ON bp.id = cl.bill_product_id
	          WHERE cl.credit_note_id = ?
	          ORDER BY cl.id`
	if docType == queue.DocDebit {
		query = `SELECT NULL, dl.name, dl.price, dl.quantity, dl.vat, 0.00,
		                dl.total_before_vat, dl.vat_total, dl.total_including_vat
		         FROM debit_note_line dl
		         WHERE dl.debit_note_id = ?
		         ORDER BY dl.id`
	}
	rows, err := db.Query(query, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []ProductRow
	for rows.Next() {
		var line ProductRow
		if err := rows.Scan(&line.ProductID, &line.Name, &line.Price, &line.Quantity, &line.VAT, &line.Discount,
			&line.TotalBeforeVAT, &line.VATTotal, &line.TotalIncludingVAT); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, rows.Err()
}

// loadBillInvoice reads the stored invoice of a bill: its UUID, and the answer that ZATCA recorded for it. It reports false when the bill
// has no stored invoice.
func loadBillInvoice(db *sql.DB, billID int64) (billInvoice, bool, error) {
	var (
		uuid    sql.NullString
		outcome string
	)
	err := db.QueryRow(`
		SELECT invoice_uuid, outcome_state FROM zatca_submission WHERE document_type = 'bill' AND source_id = ?
	`, billID).Scan(&uuid, &outcome)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return billInvoice{}, false, nil
	case err != nil:
		return billInvoice{}, false, fmt.Errorf("lookup invoice of bill %d: %w", billID, err)
	}
	return billInvoice{UUID: uuid.String, Outcome: outcome}, true, nil
}

// creditedThrough returns what the bill was credited by its credit notes up to and including the given one. The notes are taken in the
// order of their ids, which is the order in which the backend wrote them.
func creditedThrough(db *sql.DB, billID, noteID int64) (float64, error) {
	var total float64
	err := db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0) FROM credit_note WHERE bill_id = ? AND id <= ?
	`, billID, noteID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum credit notes of bill %d: %w", billID, err)
	}
	return total, nil
}

// prepareNote loads and verifies a credit or debit note against its bill and the bill's stored invoice. Each refusal is an input error:
// the note cannot be invoiced as it is, and a retry would fail the same way.
func (p *DocumentPipeline) prepareNote(_ context.Context, msg queue.Message, svc *zatca.Service, cfg *config.Config) (queue.Prepared, error) {
	note, err := LoadNote(p.DB, msg.DocType, msg.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s %d does not exist", queue.ErrRejectedInput, msg.DocType, msg.ID)
	}
	if err != nil {
		return nil, err
	}
	bill, err := QueryBillByID(p.DB, note.BillID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: the bill of %s %d does not exist", queue.ErrRejectedInput, msg.DocType, msg.ID)
	}
	if err != nil {
		return nil, err
	}
	lines, err := LoadNoteLines(p.DB, msg.DocType, msg.ID)
	if err != nil {
		return nil, fmt.Errorf("load lines of %s %d: %w", msg.DocType, msg.ID, err)
	}
	original, found, err := loadBillInvoice(p.DB, bill.ID)
	if err != nil {
		return nil, err
	}
	through := 0.0
	if msg.DocType == queue.DocCredit {
		if through, err = creditedThrough(p.DB, bill.ID, msg.ID); err != nil {
			return nil, err
		}
	}
	var stored *billInvoice
	if found {
		stored = &original
	}
	if err := verifyNote(cfg, msg, note, *bill, lines, through, stored); err != nil {
		return nil, fmt.Errorf("%w: %s %d: %v", queue.ErrRejectedInput, msg.DocType, msg.ID, err)
	}
	issueTime, err := ParseDates(note.CreatedAt, sql.NullString{}, note.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %d: %v", queue.ErrRejectedInput, msg.DocType, msg.ID, err)
	}
	return &document{
		svc:      svc,
		docType:  msg.DocType,
		sourceID: note.ID,
		billID:   bill.ID,
		build: func(booking queue.Booking) *invoice.Invoice {
			return buildNoteInvoice(cfg, msg.DocType, note, *bill, lines, *stored, issueTime, booking)
		},
	}, nil
}

// verifyNote checks a note against its bill, its lines, and the bill's stored invoice. It refuses a note that cannot be reported. The
// backend checks the same things when it writes the note; the daemon checks them again, so it never invoices a note the backend would
// have refused. The credits up to this note are the amounts of the bill's credit notes with ids up to and including this one.
func verifyNote(cfg *config.Config, msg queue.Message, note NoteRow, bill BillRow, lines []ProductRow, creditedThrough float64, stored *billInvoice) error {
	switch {
	case bill.BranchID != msg.BranchID:
		return fmt.Errorf("the bill belongs to branch %d, not %d", bill.BranchID, msg.BranchID)
	case bill.State < 1:
		return fmt.Errorf("the bill is not a final sale (state %d)", bill.State)
	case !note.State.Valid || note.State.Int64 < 1:
		return errors.New("the note is not final")
	case !note.Reason.Valid || strings.TrimSpace(note.Reason.String) == "":
		return errors.New("the note has no reason")
	case note.InvoiceUUID.Valid && note.InvoiceUUID.String != msg.InvoiceUUID:
		return errors.New("the note carries another invoice UUID")
	case cfg.Seller.VAT == "":
		return errors.New("the branch has no seller VAT number")
	case cfg.EGSID == "":
		return errors.New("the branch has no EGS id: onboard the branch first")
	case note.Amount <= 0:
		return errors.New("the amount is not above zero")
	case len(lines) == 0:
		return errors.New("the note has no lines")
	case stored == nil:
		return errors.New("the bill has no ZATCA invoice to refer to")
	case stored.Outcome != queue.OutcomeAccepted && stored.Outcome != queue.OutcomeWarning:
		return fmt.Errorf("the bill's invoice is not accepted by ZATCA (outcome %q)", stored.Outcome)
	case msg.DocType == queue.DocCredit && creditedThrough > bill.Total+noteTolerance:
		return fmt.Errorf("the credits up to this note exceed the bill total of %.2f", bill.Total)
	}

	sum := 0.0
	for i, line := range lines {
		if line.QuantityOrZero() <= 0 {
			return fmt.Errorf("line %d has no positive quantity", i+1)
		}
		if line.PriceOrZero() < 0 {
			return fmt.Errorf("line %d has a negative price", i+1)
		}
		sum += line.TotalIncludingVAT.Float64
	}
	if math.Abs(sum-note.Amount) > noteTolerance {
		return fmt.Errorf("the amount %.2f is not the total of the lines %.2f", note.Amount, sum)
	}
	return nil
}

// buildNoteInvoice builds a credit or debit note under a booking. The note is numbered CN-<id> or DN-<id>, until the branch code is
// decided (D10). It refers to the original invoice by its number and its UUID, and it carries the reason as its instruction note. The
// booked UUID, ICV, and previous hash are the only fiscal values that it takes from the booking.
func buildNoteInvoice(cfg *config.Config, docType queue.DocType, note NoteRow, bill BillRow, lines []ProductRow,
	original billInvoice, issueTime time.Time, booking queue.Booking) *invoice.Invoice {
	isStandard := bill.BuyerVAT.Valid && bill.BuyerVAT.String != ""
	typeCode, prefix := invoice.TypeCreditNote, "CN"
	if docType == queue.DocDebit {
		typeCode, prefix = invoice.TypeDebitNote, "DN"
	}
	inv := &invoice.Invoice{
		ID:                   fmt.Sprintf("%s-%d", prefix, note.ID),
		UUID:                 booking.UUID,
		IssueDate:            issueTime,
		IssueTime:            issueTime,
		TypeCode:             typeCode,
		DocumentCurrency:     "SAR",
		TaxCurrency:          "SAR",
		Supplier:             supplierOf(cfg),
		Customer:             buildCustomer(bill, isStandard),
		BillingReferenceID:   fmt.Sprintf("INV-%d", bill.ID),
		BillingReferenceUUID: original.UUID,
		PreviousInvoiceHash:  booking.PIH,
		InvoiceCounterValue:  int(booking.ICV),
		PaymentMeans:         paymentMeans(bill),
		InstructionNote:      note.Reason.String,
		Lines:                buildNoteLines(lines),
	}
	if isStandard {
		inv.SubType = invoice.SubTypeStandard
	} else {
		inv.SubType = invoice.SubTypeSimplified
	}
	return inv
}

// buildNoteLines converts the lines of a note into invoice lines. Unlike the lines of a bill, a note's lines keep their discount. The
// note's total is the total of its lines, and they are the bill's lines in part.
func buildNoteLines(lines []ProductRow) []invoice.LineItem {
	items := buildLines(lines)
	for i := range items {
		items[i].Discount = lines[i].Discount.Float64
	}
	return items
}
