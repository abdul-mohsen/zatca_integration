package processor

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zatca-go/zatca/client"
	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/models"
	"github.com/zatca-go/zatca/queue"
)

const testInvoiceUUID = "0c4f7a1e-2b3d-4e5f-8a9b-1c2d3e4f5a6b"

func verifiedConfig() *config.Config {
	return &config.Config{Seller: config.SellerInfo{VAT: "300000000000003"}, EGSID: "egs-uuid-7"}
}

func lineOf(quantity, price float64) ProductRow {
	return ProductRow{
		Name:     sql.NullString{String: "Brake pad", Valid: true},
		Quantity: sql.NullFloat64{Float64: quantity, Valid: true},
		Price:    sql.NullFloat64{Float64: price, Valid: true},
		VAT:      sql.NullFloat64{Float64: 15, Valid: true},
	}
}

// verifiedBill is a final sale of branch 7 that already carries the envelope's invoice UUID.
func verifiedBill() (queue.Message, BillRow) {
	msg := queue.Message{DocType: queue.DocBill, ID: 42, BranchID: 7, InvoiceUUID: testInvoiceUUID}
	bill := BillRow{
		ID:          42,
		BranchID:    7,
		State:       1,
		SeqNumber:   sql.NullString{String: "BR-007-0042", Valid: true},
		InvoiceUUID: sql.NullString{String: testInvoiceUUID, Valid: true},
	}
	return msg, bill
}

func TestVerifyBillRefusesWhatCannotBeReported(t *testing.T) {
	cases := map[string]struct {
		cfg      *config.Config
		products []ProductRow
		edit     func(*BillRow)
	}{
		"no seller VAT":            {cfg: &config.Config{EGSID: "egs-uuid-7"}, products: []ProductRow{lineOf(1, 10)}},
		"no EGS id":                {cfg: &config.Config{Seller: config.SellerInfo{VAT: "300000000000003"}}, products: []ProductRow{lineOf(1, 10)}},
		"no lines":                 {cfg: verifiedConfig()},
		"zero quantity":            {cfg: verifiedConfig(), products: []ProductRow{lineOf(0, 10)}},
		"negative price":           {cfg: verifiedConfig(), products: []ProductRow{lineOf(1, -10)}},
		"missing printed identity": {cfg: verifiedConfig(), products: []ProductRow{lineOf(1, 10)}, edit: func(b *BillRow) { b.SeqNumber = sql.NullString{} }},
		"blank printed identity":   {cfg: verifiedConfig(), products: []ProductRow{lineOf(1, 10)}, edit: func(b *BillRow) { b.SeqNumber = sql.NullString{String: "   ", Valid: true} }},
		"a bill of another branch": {cfg: verifiedConfig(), products: []ProductRow{lineOf(1, 10)}, edit: func(b *BillRow) { b.BranchID = 8 }},
		"a draft":                  {cfg: verifiedConfig(), products: []ProductRow{lineOf(1, 10)}, edit: func(b *BillRow) { b.State = 0 }},
		"a deleted bill":           {cfg: verifiedConfig(), products: []ProductRow{lineOf(1, 10)}, edit: func(b *BillRow) { b.State = -1 }},
		"another invoice UUID": {cfg: verifiedConfig(), products: []ProductRow{lineOf(1, 10)}, edit: func(b *BillRow) {
			b.InvoiceUUID = sql.NullString{String: "11111111-2222-4333-8444-555555555555", Valid: true}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg, bill := verifiedBill()
			if tc.edit != nil {
				tc.edit(&bill)
			}
			if err := verifyBill(tc.cfg, msg, bill, tc.products); err == nil {
				t.Fatal("verifyBill accepted a bill that cannot be reported")
			}
		})
	}
}

func TestVerifyBillAcceptsAFinalSaleOfTheBranch(t *testing.T) {
	msg, bill := verifiedBill()
	if err := verifyBill(verifiedConfig(), msg, bill, []ProductRow{lineOf(2, 10)}); err != nil {
		t.Fatalf("verifyBill refused a valid bill: %v", err)
	}
	// A bill that has no invoice UUID yet is valid: the sale stores it at commit.
	bill.InvoiceUUID = sql.NullString{}
	if err := verifyBill(verifiedConfig(), msg, bill, []ProductRow{lineOf(2, 10)}); err != nil {
		t.Fatalf("verifyBill refused a bill without an invoice UUID yet: %v", err)
	}
}

// The invoice takes its XML ID from the printed branch sequence and its UUID,
// ICV, and previous hash from the booking. The bill row ID is not a document
// identity, and the printed sequence is never an ICV.
func TestBuildBillInvoiceUsesTheBookingAndNothingElse(t *testing.T) {
	booking := queue.Booking{
		BranchID: 7,
		UUID:     testInvoiceUUID,
		ICV:      12,
		PIH:      "previous-hash",
	}
	issued := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	bill := BillRow{ID: 42, SeqNumber: sql.NullString{String: "BR-007-0042", Valid: true}}

	inv := buildBillInvoice(verifiedConfig(), bill, []ProductRow{lineOf(1, 10)}, issued, booking)
	if inv.ID != "BR-007-0042" {
		t.Fatalf("invoice ID = %q; want the branch/printed sequence", inv.ID)
	}
	if inv.ID == fmt.Sprintf("INV-%d", bill.ID) || inv.ID == strconv.FormatUint(booking.ICV, 10) {
		t.Fatalf("invoice ID = %q; must not be the bill row ID or ICV", inv.ID)
	}
	xml, err := inv.ToXML()
	if err != nil {
		t.Fatalf("ToXML: %v", err)
	}
	if !strings.Contains(xml, "<cbc:ID>BR-007-0042</cbc:ID>") {
		t.Fatalf("XML does not preserve the printed document identity: %s", xml)
	}
	if inv.UUID != booking.UUID || inv.InvoiceCounterValue != 12 || inv.PreviousInvoiceHash != "previous-hash" {
		t.Fatalf("invoice = UUID %s, ICV %d, PIH %s; want the booking's values", inv.UUID, inv.InvoiceCounterValue, inv.PreviousInvoiceHash)
	}
}

// ZATCA can answer 2xx and still list errors, which reject the invoice. Warnings accept it with a warning.
func TestSuccessfulAnswerIsClassifiedByItsMessages(t *testing.T) {
	cases := map[string]struct {
		resp *models.InvoiceResponse
		want queue.Outcome
	}{
		"error messages reject the invoice": {
			resp: &models.InvoiceResponse{ValidationResults: &models.ValidationResults{ErrorMessages: []models.ValidationMessage{
				{Code: "BR-KSA-61", Message: "seller VAT is missing"},
				{Code: "BR-KSA-62", Message: "buyer name is missing"},
			}}},
			want: queue.Outcome{State: queue.OutcomeRejected, ProviderCode: "BR-KSA-61", Message: "seller VAT is missing; buyer name is missing"},
		},
		"top-level errors reject the invoice": {
			resp: &models.InvoiceResponse{Errors: []models.ErrorDetail{
				{Code: "INV-ERROR", Message: "invoice is invalid"},
				{Code: "INV-ERROR-2", Message: "invoice cannot be reported"},
			}},
			want: queue.Outcome{State: queue.OutcomeRejected, ProviderCode: "INV-ERROR", Message: "invoice is invalid; invoice cannot be reported"},
		},
		"failed validation status rejects the invoice": {
			resp: &models.InvoiceResponse{ValidationResults: &models.ValidationResults{Status: "ERROR"}},
			want: queue.Outcome{State: queue.OutcomeRejected, ProviderCode: "ERROR", Message: "provider validation status: ERROR"},
		},
		"errors win over warnings": {
			resp: &models.InvoiceResponse{ValidationResults: &models.ValidationResults{
				WarningMessages: []models.ValidationMessage{{Code: "BR-KSA-W1", Message: "note is long"}},
				ErrorMessages:   []models.ValidationMessage{{Code: "BR-KSA-61", Message: "seller VAT is missing"}},
			}},
			want: queue.Outcome{State: queue.OutcomeRejected, ProviderCode: "BR-KSA-61", Message: "seller VAT is missing"},
		},
		"warning messages accept with a warning": {
			resp: &models.InvoiceResponse{ValidationResults: &models.ValidationResults{
				WarningMessages: []models.ValidationMessage{{Code: "BR-KSA-W1", Message: "note is long"}},
			}},
			want: queue.Outcome{State: queue.OutcomeWarning, ProviderCode: "BR-KSA-W1", Message: "note is long"},
		},
		"warnings accept with a warning": {
			resp: &models.InvoiceResponse{Warnings: []models.ErrorDetail{{Code: "W1", Message: "late"}}},
			want: queue.Outcome{State: queue.OutcomeWarning, ProviderCode: "W1", Message: "late"},
		},
		"warning validation status accepts with a warning": {
			resp: &models.InvoiceResponse{ValidationResults: &models.ValidationResults{Status: "WARNING"}},
			want: queue.Outcome{State: queue.OutcomeWarning, ProviderCode: "WARNING", Message: "provider validation status: WARNING"},
		},
		"neither accepts": {
			resp: &models.InvoiceResponse{ValidationResults: &models.ValidationResults{Status: "PASS"}},
			want: queue.Outcome{State: queue.OutcomeAccepted},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := responseOutcome(tc.resp); got != tc.want {
				t.Fatalf("responseOutcome = %+v; want %+v", got, tc.want)
			}
		})
	}
}

// A 409 means ZATCA already holds the invoice, for example after a crash. The answer is ambiguous, not a rejection, and the
// error names the bill and says it needs reconciliation, so the operator is alerted.
func TestConflictIsAmbiguousAndNamesTheBill(t *testing.T) {
	invoice := queue.SignedInvoice{SourceID: 42, UUID: testInvoiceUUID}
	answer := fmt.Errorf("ReportInvoice: %w", &client.APIError{StatusCode: http.StatusConflict, Body: "already reported"})
	outcome, err := rejectionOutcome(invoice, answer)
	if outcome != (queue.Outcome{State: queue.OutcomeAmbiguous, ProviderCode: "409", Message: "already reported"}) {
		t.Fatalf("outcome = %+v; want an ambiguous outcome with code 409", outcome)
	}
	if !errors.Is(err, queue.ErrOutcomeAmbiguous) {
		t.Fatalf("err = %v; want ErrOutcomeAmbiguous", err)
	}
	for _, want := range []string{"bill 42", testInvoiceUUID, "reconciliation"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q; want it to contain %q", err, want)
		}
	}
}

// Other 4xx answers reject the invoice. Authentication, timeouts, rate limits, and server errors stay technical.
func TestRejectionOutcomeOnlyForInvoiceRejections(t *testing.T) {
	invoice := queue.SignedInvoice{SourceID: 42, UUID: testInvoiceUUID}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		outcome, err := rejectionOutcome(invoice, &client.APIError{StatusCode: status, Body: "NOT_VALID"})
		if err != nil || outcome != (queue.Outcome{State: queue.OutcomeRejected, ProviderCode: strconv.Itoa(status), Message: "NOT_VALID"}) {
			t.Fatalf("HTTP %d = %+v, %v; want a rejection", status, outcome, err)
		}
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError} {
		outcome, err := rejectionOutcome(invoice, &client.APIError{StatusCode: status})
		if err == nil || outcome != (queue.Outcome{}) || errors.Is(err, queue.ErrOutcomeAmbiguous) {
			t.Fatalf("HTTP %d = %+v, %v; want a technical error with no outcome", status, outcome, err)
		}
	}
}
