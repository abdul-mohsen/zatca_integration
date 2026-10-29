package processor

import (
	"database/sql"
	_ "embed"
	"encoding/xml"
	"testing"
	"time"

	"github.com/zatca-go/zatca/config"
)

//go:embed testdata/t019_party_identification.xml
var t019PartyFixture []byte

//go:embed testdata/t018_discounted_invoice.xml
var t018DiscountedInvoiceFixture []byte

type t019GeneratedID struct {
	SchemeID string `xml:"schemeID,attr"`
	Value    string `xml:",chardata"`
}

type t019GeneratedPartyIdentification struct {
	ID t019GeneratedID `xml:"ID"`
}

type t019GeneratedParty struct {
	Identifications []t019GeneratedPartyIdentification `xml:"PartyIdentification"`
}

type t019GeneratedAccountingParty struct {
	Party t019GeneratedParty `xml:"Party"`
}

type t019GeneratedInvoice struct {
	Supplier t019GeneratedAccountingParty `xml:"AccountingSupplierParty"`
	Customer t019GeneratedAccountingParty `xml:"AccountingCustomerParty"`
}

type t018Amount struct {
	Value string `xml:",chardata"`
}

type t018TaxSubtotal struct {
	TaxableAmount t018Amount `xml:"TaxableAmount"`
	TaxAmount     t018Amount `xml:"TaxAmount"`
}

type t018TaxTotal struct {
	TaxAmount t018Amount      `xml:"TaxAmount"`
	Subtotal  t018TaxSubtotal `xml:"TaxSubtotal"`
}

type t018Allowance struct {
	ChargeIndicator string     `xml:"ChargeIndicator"`
	Amount          t018Amount `xml:"Amount"`
}

type t018Price struct {
	PriceAmount t018Amount    `xml:"PriceAmount"`
	Allowance   t018Allowance `xml:"AllowanceCharge"`
}

type t018InvoiceLine struct {
	LineExtensionAmount t018Amount    `xml:"LineExtensionAmount"`
	TaxTotal            t018TaxTotal  `xml:"TaxTotal"`
	Price               t018Price     `xml:"Price"`
	Allowance           t018Allowance `xml:"AllowanceCharge"`
}

type t018LegalMonetaryTotal struct {
	LineExtensionAmount  t018Amount `xml:"LineExtensionAmount"`
	TaxExclusiveAmount   t018Amount `xml:"TaxExclusiveAmount"`
	TaxInclusiveAmount   t018Amount `xml:"TaxInclusiveAmount"`
	AllowanceTotalAmount t018Amount `xml:"AllowanceTotalAmount"`
	PayableAmount        t018Amount `xml:"PayableAmount"`
}

type t018GeneratedInvoice struct {
	Allowances []t018Allowance        `xml:"AllowanceCharge"`
	TaxTotals  []t018TaxTotal         `xml:"TaxTotal"`
	Legal      t018LegalMonetaryTotal `xml:"LegalMonetaryTotal"`
	Lines      []t018InvoiceLine      `xml:"InvoiceLine"`
}

func TestBuildBillInvoicePartyIdentificationFixture(t *testing.T) {
	var want t019GeneratedInvoice
	if err := xml.Unmarshal(t019PartyFixture, &want); err != nil {
		t.Fatalf("unmarshal party fixture: %v", err)
	}

	inv := BuildBillInvoice(
		&config.Config{
			Seller: config.SellerInfo{
				Name:    "Seller",
				VAT:     "399999999900003",
				CRN:     "1010010000",
				Country: "SA",
			},
		},
		BillRow{
			ID: 1,
			BuyerVAT: sql.NullString{
				String: "399999999800003",
				Valid:  true,
			},
			BuyerSchemeID: sql.NullString{
				String: "CRN",
				Valid:  true,
			},
			BuyerRegistration: sql.NullString{
				String: "3020000000",
				Valid:  true,
			},
		},
		[]ProductRow{{
			Name:     sql.NullString{String: "Item", Valid: true},
			Price:    sql.NullFloat64{Float64: 10, Valid: true},
			Quantity: sql.NullFloat64{Float64: 1, Valid: true},
			VAT:      sql.NullFloat64{Float64: 15, Valid: true},
		}},
		time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
	)

	generated, err := inv.ToXML()
	if err != nil {
		t.Fatalf("ToXML() error: %v", err)
	}

	var document t019GeneratedInvoice
	if err := xml.Unmarshal([]byte(generated), &document); err != nil {
		t.Fatalf("unmarshal generated invoice: %v", err)
	}

	comparePartyIdentification := func(role string, got, want t019GeneratedAccountingParty) {
		if len(got.Party.Identifications) != len(want.Party.Identifications) {
			t.Errorf("%s party identification count = %d; want %d",
				role, len(got.Party.Identifications), len(want.Party.Identifications))
			return
		}

		for i, gotIdentification := range got.Party.Identifications {
			gotID := gotIdentification.ID
			wantID := want.Party.Identifications[i].ID
			if gotID.SchemeID != wantID.SchemeID || gotID.Value != wantID.Value {
				t.Errorf("%s party identification = schemeID %q, ID %q; want schemeID %q, ID %q",
					role, gotID.SchemeID, gotID.Value, wantID.SchemeID, wantID.Value)
			}
		}
	}

	comparePartyIdentification("seller", document.Supplier, want.Supplier)
	comparePartyIdentification("customer", document.Customer, want.Customer)
}

func TestBuildBillInvoiceDiscountedXMLFixture(t *testing.T) {
	var want t018GeneratedInvoice
	if err := xml.Unmarshal(t018DiscountedInvoiceFixture, &want); err != nil {
		t.Fatalf("unmarshal discount fixture: %v", err)
	}

	inv := BuildBillInvoice(
		&config.Config{
			Seller: config.SellerInfo{
				Name:    "Seller",
				VAT:     "399999999900003",
				CRN:     "1010010000",
				Country: "SA",
			},
		},
		BillRow{
			ID:       18,
			Discount: sql.NullFloat64{Float64: 10, Valid: true},
		},
		[]ProductRow{{
			Name:     sql.NullString{String: "Discounted item", Valid: true},
			Price:    sql.NullFloat64{Float64: 100, Valid: true},
			Quantity: sql.NullFloat64{Float64: 2, Valid: true},
			VAT:      sql.NullFloat64{Float64: 15, Valid: true},
			Discount: sql.NullFloat64{Float64: 10, Valid: true},
		}},
		time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
	)

	if inv.Discount != 10 {
		t.Fatalf("invoice discount = %v; want 10", inv.Discount)
	}
	if len(inv.Lines) != 1 || inv.Lines[0].Discount != 10 {
		t.Fatalf("line discount = %#v; want 10", inv.Lines)
	}

	generated, err := inv.ToXML()
	if err != nil {
		t.Fatalf("ToXML() error: %v", err)
	}

	var got t018GeneratedInvoice
	if err := xml.Unmarshal([]byte(generated), &got); err != nil {
		t.Fatalf("unmarshal generated invoice: %v", err)
	}

	if len(got.TaxTotals) != len(want.TaxTotals) {
		t.Fatalf("tax total count = %d; want %d", len(got.TaxTotals), len(want.TaxTotals))
	}
	if len(got.Allowances) != len(want.Allowances) {
		t.Fatalf("document allowance count = %d; want %d", len(got.Allowances), len(want.Allowances))
	}
	if len(got.Allowances) != 1 {
		t.Fatalf("document allowance count = %d; want 1", len(got.Allowances))
	}
	compareAmount := func(name, got, want string) {
		if got != want {
			t.Errorf("%s = %s; want %s", name, got, want)
		}
	}
	compareAmount("document allowance", got.Allowances[0].Amount.Value, want.Allowances[0].Amount.Value)
	if got.Allowances[0].ChargeIndicator != want.Allowances[0].ChargeIndicator {
		t.Errorf("document allowance charge indicator = %q; want %q",
			got.Allowances[0].ChargeIndicator, want.Allowances[0].ChargeIndicator)
	}
	if got.TaxTotals[0].TaxAmount.Value != want.TaxTotals[0].TaxAmount.Value {
		t.Errorf("summary tax = %s; want %s", got.TaxTotals[0].TaxAmount.Value, want.TaxTotals[0].TaxAmount.Value)
	}
	if got.TaxTotals[1].Subtotal.TaxableAmount.Value != want.TaxTotals[1].Subtotal.TaxableAmount.Value {
		t.Errorf("tax subtotal taxable = %s; want %s",
			got.TaxTotals[1].Subtotal.TaxableAmount.Value, want.TaxTotals[1].Subtotal.TaxableAmount.Value)
	}
	if got.TaxTotals[1].Subtotal.TaxAmount.Value != want.TaxTotals[1].Subtotal.TaxAmount.Value {
		t.Errorf("tax subtotal tax = %s; want %s",
			got.TaxTotals[1].Subtotal.TaxAmount.Value, want.TaxTotals[1].Subtotal.TaxAmount.Value)
	}

	compareAmount("line extension", got.Legal.LineExtensionAmount.Value, want.Legal.LineExtensionAmount.Value)
	compareAmount("tax exclusive", got.Legal.TaxExclusiveAmount.Value, want.Legal.TaxExclusiveAmount.Value)
	compareAmount("tax inclusive", got.Legal.TaxInclusiveAmount.Value, want.Legal.TaxInclusiveAmount.Value)
	compareAmount("allowance total", got.Legal.AllowanceTotalAmount.Value, want.Legal.AllowanceTotalAmount.Value)
	compareAmount("payable", got.Legal.PayableAmount.Value, want.Legal.PayableAmount.Value)

	if len(got.Lines) != 1 {
		t.Fatalf("invoice line count = %d; want 1", len(got.Lines))
	}
	compareAmount("invoice line extension",
		got.Lines[0].LineExtensionAmount.Value, want.Lines[0].LineExtensionAmount.Value)
	compareAmount("invoice line tax",
		got.Lines[0].TaxTotal.TaxAmount.Value, want.Lines[0].TaxTotal.TaxAmount.Value)
	compareAmount("invoice line price",
		got.Lines[0].Price.PriceAmount.Value, want.Lines[0].Price.PriceAmount.Value)
	compareAmount("line allowance", got.Lines[0].Allowance.Amount.Value, want.Lines[0].Allowance.Amount.Value)
	if got.Lines[0].Allowance.ChargeIndicator != want.Lines[0].Allowance.ChargeIndicator {
		t.Errorf("line allowance charge indicator = %q; want %q",
			got.Lines[0].Allowance.ChargeIndicator, want.Lines[0].Allowance.ChargeIndicator)
	}
}
