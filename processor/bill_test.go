package processor

import (
	"database/sql"
	"testing"
	"time"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/queue"
)

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

func TestVerifyBillRefusesWhatCannotBeReported(t *testing.T) {
	cases := map[string]struct {
		cfg      *config.Config
		products []ProductRow
	}{
		"no seller VAT":  {&config.Config{EGSID: "egs-uuid-7"}, []ProductRow{lineOf(1, 10)}},
		"no EGS id":      {&config.Config{Seller: config.SellerInfo{VAT: "300000000000003"}}, []ProductRow{lineOf(1, 10)}},
		"no lines":       {verifiedConfig(), nil},
		"zero quantity":  {verifiedConfig(), []ProductRow{lineOf(0, 10)}},
		"negative price": {verifiedConfig(), []ProductRow{lineOf(1, -10)}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := verifyBill(tc.cfg, tc.products); err == nil {
				t.Fatal("verifyBill accepted a bill that cannot be reported")
			}
		})
	}
	if err := verifyBill(verifiedConfig(), []ProductRow{lineOf(2, 10)}); err != nil {
		t.Fatalf("verifyBill refused a valid bill: %v", err)
	}
}

// The invoice takes its UUID, ICV, and previous hash from the booking. The bill's own sequence number is never an ICV.
func TestBuildBillInvoiceUsesTheBookingAndNothingElse(t *testing.T) {
	booking := queue.Booking{
		BranchID: 7,
		UUID:     "0c4f7a1e-2b3d-4e5f-8a9b-1c2d3e4f5a6b",
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
