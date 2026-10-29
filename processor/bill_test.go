package processor

import (
	"database/sql"
	"encoding/xml"
	"testing"
	"time"

	_ "embed"

	"github.com/zatca-go/zatca/config"
)

//go:embed testdata/t019_party_identification.xml
var t019PartyFixture []byte

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
