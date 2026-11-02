package processor

import (
	"database/sql"
	"testing"
	"time"

	"github.com/zatca-go/zatca/config"
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

// The invoice takes its UUID, ICV, and previous hash from the booking. The bill's own sequence number is never an ICV.
func TestBuildBillInvoiceUsesTheBookingAndNothingElse(t *testing.T) {
	booking := queue.Booking{
		BranchID: 7,
		UUID:     testInvoiceUUID,
		ICV:      12,
		PIH:      "previous-hash",
	}
	issued := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	bill := BillRow{ID: 42, SeqNumber: sql.NullString{String: "999", Valid: true}}

	inv := buildBillInvoice(verifiedConfig(), bill, []ProductRow{lineOf(1, 10)}, issued, booking)
	if inv.UUID != booking.UUID || inv.InvoiceCounterValue != 12 || inv.PreviousInvoiceHash != "previous-hash" {
		t.Fatalf("invoice = UUID %s, ICV %d, PIH %s; want the booking's values", inv.UUID, inv.InvoiceCounterValue, inv.PreviousInvoiceHash)
	}
}
