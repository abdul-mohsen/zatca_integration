package processor

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/queue"
)

const testNoteUUID = "8a2b4c6d-1e3f-4a5b-9c7d-0e2f4a6b8c1d"

func nf(v float64) sql.NullFloat64 {
	return sql.NullFloat64{Float64: v, Valid: true}
}

// noteCase is the input of verifyNote: a note, its bill, its lines, the credits up to it, and the bill's stored invoice.
type noteCase struct {
	msg     queue.Message
	note    NoteRow
	bill    BillRow
	lines   []ProductRow
	through float64
	stored  *billInvoice
}

// creditFixture is a credit note of 11.50 on bill 42 of branch 7. The bill has a stored invoice that ZATCA accepted, and the credit is
// within the bill total of 40.25.
func creditFixture() noteCase {
	return noteCase{
		msg: queue.Message{DocType: queue.DocCredit, ID: 9, BranchID: 7, InvoiceUUID: testNoteUUID},
		note: NoteRow{
			ID:          9,
			BillID:      42,
			State:       sql.NullInt64{Int64: 1, Valid: true},
			Reason:      sql.NullString{String: "returned", Valid: true},
			Amount:      11.50,
			LinesTotal:  11.50,
			InvoiceUUID: sql.NullString{String: testNoteUUID, Valid: true},
			CreatedAt:   sql.NullString{String: "2026-11-02 10:00:00", Valid: true},
		},
		bill: BillRow{
			ID:       42,
			BranchID: 7,
			State:    3,
			Total:    40.25,
			BuyerVAT: sql.NullString{String: "310000000000003", Valid: true},
			UserName: sql.NullString{String: "Buyer", Valid: true},
		},
		lines: []ProductRow{{
			Name:              sql.NullString{String: "Brake pad", Valid: true},
			Price:             nf(10),
			Quantity:          nf(1),
			VAT:               nf(15),
			Discount:          nf(0),
			TotalBeforeVAT:    nf(10),
			VATTotal:          nf(1.5),
			TotalIncludingVAT: nf(11.5),
		}},
		through: 11.50,
		stored:  &billInvoice{UUID: testInvoiceUUID, Outcome: queue.OutcomeAccepted},
	}
}

func TestVerifyNoteRefusesWhatCannotBeReported(t *testing.T) {
	cases := map[string]struct {
		edit func(*noteCase)
		want string
	}{
		"another branch": {func(c *noteCase) { c.bill.BranchID = 8 }, "belongs to branch"},
		"a draft bill":   {func(c *noteCase) { c.bill.State = 0 }, "not a final sale"},
		"a draft note":   {func(c *noteCase) { c.note.State = sql.NullInt64{} }, "not final"},
		"no reason":      {func(c *noteCase) { c.note.Reason = sql.NullString{} }, "no reason"},
		"another invoice UUID": {func(c *noteCase) {
			c.note.InvoiceUUID = sql.NullString{String: "11111111-2222-4333-8444-555555555555", Valid: true}
		}, "another invoice UUID"},
		"zero amount":                 {func(c *noteCase) { c.note.Amount = 0 }, "not above zero"},
		"no lines":                    {func(c *noteCase) { c.lines = nil }, "no lines"},
		"a line with no quantity":     {func(c *noteCase) { c.lines[0].Quantity = nf(0) }, "no positive quantity"},
		"amount not the lines' total": {func(c *noteCase) { c.note.Amount = 11 }, "not the total of the lines"},
		"no stored invoice":           {func(c *noteCase) { c.stored = nil }, "no ZATCA invoice"},
		"a rejected bill invoice":     {func(c *noteCase) { c.stored.Outcome = queue.OutcomeRejected }, "not accepted"},
		"a pending bill invoice":      {func(c *noteCase) { c.stored.Outcome = "pending" }, "not accepted"},
		"credits above the total":     {func(c *noteCase) { c.through = 41 }, "exceed the bill total"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := creditFixture()
			tc.edit(&c)
			err := verifyNote(verifiedConfig(), c.msg, c.note, c.bill, c.lines, c.through, c.stored)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("verifyNote = %v; want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestVerifyNoteAcceptsAFinalCreditNoteWithinTheBill(t *testing.T) {
	c := creditFixture()
	if err := verifyNote(verifiedConfig(), c.msg, c.note, c.bill, c.lines, c.through, c.stored); err != nil {
		t.Fatalf("verifyNote refused a valid credit note: %v", err)
	}
	// A debit note is not limited by the bill total, so the credits up to it do not matter.
	c.msg.DocType = queue.DocDebit
	if err := verifyNote(verifiedConfig(), c.msg, c.note, c.bill, c.lines, 0, c.stored); err != nil {
		t.Fatalf("verifyNote refused a valid debit note: %v", err)
	}
}

// A credit note is invoiced with type 381, and a debit note with 383. Each refers to the original invoice by its number and its UUID,
// carries the reason as its instruction note, and takes only the booking's fiscal values from the booking.
func TestBuildNoteInvoiceReferencesTheOriginalInvoiceAndCarriesTheReason(t *testing.T) {
	cases := map[string]struct {
		docType  queue.DocType
		id       string
		typeCode invoice.TypeCode
		xmlCode  string
	}{
		"credit": {queue.DocCredit, "CN-9", invoice.TypeCreditNote, "381"},
		"debit":  {queue.DocDebit, "DN-9", invoice.TypeDebitNote, "383"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := creditFixture()
			booking := queue.Booking{BranchID: 7, UUID: testNoteUUID, ICV: 13, PIH: "previous-hash"}
			issued := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
			inv := buildNoteInvoice(verifiedConfig(), tc.docType, c.note, c.bill, c.lines, *c.stored, issued, booking)

			if inv.ID != tc.id || inv.TypeCode != tc.typeCode || inv.UUID != testNoteUUID {
				t.Fatalf("invoice = ID %s, type %d, UUID %s; want %s, type %d, UUID %s", inv.ID, inv.TypeCode, inv.UUID, tc.id, tc.typeCode, testNoteUUID)
			}
			if inv.InvoiceCounterValue != 13 || inv.PreviousInvoiceHash != "previous-hash" {
				t.Fatalf("chain = ICV %d, PIH %q; want the booking's 13 and previous-hash", inv.InvoiceCounterValue, inv.PreviousInvoiceHash)
			}
			if inv.BillingReferenceID != "INV-42" || inv.BillingReferenceUUID != testInvoiceUUID || inv.InstructionNote != "returned" {
				t.Fatalf("billing reference %q %q and instruction note %q; want INV-42, the stored UUID, and the reason",
					inv.BillingReferenceID, inv.BillingReferenceUUID, inv.InstructionNote)
			}

			xml, err := inv.ToXML()
			if err != nil {
				t.Fatalf("ToXML: %v", err)
			}
			for _, want := range []string{
				`<cbc:InvoiceTypeCode name="0100000">` + tc.xmlCode + `</cbc:InvoiceTypeCode>`,
				"<cac:BillingReference>\n        <cac:InvoiceDocumentReference>\n            <cbc:ID>INV-42</cbc:ID>\n            <cbc:UUID>" + testInvoiceUUID + "</cbc:UUID>",
				"<cbc:InstructionNote>returned</cbc:InstructionNote>",
			} {
				if !strings.Contains(xml, want) {
					t.Fatalf("XML does not contain %q", want)
				}
			}
		})
	}
}

// A note's lines keep their discount, because its total is the total of its lines. The bill's lines do not, as before.
func TestNoteLinesKeepTheirDiscountAndBillLinesDoNot(t *testing.T) {
	products := []ProductRow{{Name: sql.NullString{String: "Brake pad", Valid: true}, Price: nf(10), Quantity: nf(1), VAT: nf(15), Discount: nf(1.5)}}
	if got := buildNoteLines(products)[0].Discount; got != 1.5 {
		t.Fatalf("note line discount = %v; want 1.5", got)
	}
	if got := buildLines(products)[0].Discount; got != 0 {
		t.Fatalf("bill line discount = %v; want 0, as the bill mapping had it before B6", got)
	}
}
