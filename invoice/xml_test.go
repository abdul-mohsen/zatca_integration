package invoice

import (
	"strings"
	"testing"
	"time"
)

func sampleInvoice() *Invoice {
	return &Invoice{
		ID:        "SME00023",
		UUID:      "8d487816-70b8-4ade-a618-9d620b73814a",
		IssueDate: time.Date(2022, 9, 7, 0, 0, 0, 0, time.UTC),
		IssueTime: time.Date(0, 1, 1, 12, 21, 28, 0, time.UTC),
		TypeCode:  TypeInvoice,
		SubType:   SubTypeStandard,
		Supplier: Party{
			RegistrationName:   "Maximum Speed Tech Supply LTD",
			RegistrationNameAR: "شركة توريد التكنولوجيا بأقصى سرعة المحدودة",
			VAT:                "399999999900003",
			SchemeID:           "CRN",
			ID:                 "1010010000",
			Street:             "Prince Sultan",
			Building:           "2322",
			District:           "Al-Murabba",
			City:               "Riyadh",
			PostalCode:         "23333",
			Country:            "SA",
		},
		Customer: Party{
			RegistrationName: "Fatoora Samples LTD",
			VAT:              "399999999800003",
			Street:           "Salah Al-Din",
			Building:         "1111",
			District:         "Al-Murooj",
			City:             "Riyadh",
			PostalCode:       "12222",
			Country:          "SA",
		},
		DeliveryDate:        time.Date(2022, 9, 7, 0, 0, 0, 0, time.UTC),
		PaymentMeans:        PaymentCash,
		PreviousInvoiceHash: "NWZlY2ViNjZmZmM4NmYzOGQ5NTI3ODZjNmQ2OTZjNzljMmRiYzIzOWRkNGU5MWI0NjcyOWQ3M2EyN2ZiNTdlOQ==",
		InvoiceCounterValue: 23,
		Lines: []LineItem{
			{
				Name:        "قلم رصاص",
				Quantity:    2,
				UnitCode:    "PCE",
				UnitPrice:   2.00,
				TaxCategory: TaxStandard,
				TaxPercent:  15.00,
			},
		},
	}
}

func TestToXML(t *testing.T) {
	inv := sampleInvoice()
	xml, err := inv.ToXML()
	if err != nil {
		t.Fatalf("ToXML() error: %v", err)
	}

	// Check required elements exist
	checks := []string{
		`<cbc:ProfileID>reporting:1.0</cbc:ProfileID>`,
		`<cbc:ID>SME00023</cbc:ID>`,
		`<cbc:UUID>8d487816-70b8-4ade-a618-9d620b73814a</cbc:UUID>`,
		`<cbc:IssueDate>2022-09-07</cbc:IssueDate>`,
		`<cbc:IssueTime>12:21:28</cbc:IssueTime>`,
		`<cbc:InvoiceTypeCode name="0100000">388</cbc:InvoiceTypeCode>`,
		`<cbc:DocumentCurrencyCode>SAR</cbc:DocumentCurrencyCode>`,
		`<cbc:CompanyID>399999999900003</cbc:CompanyID>`,
		`<cbc:CompanyID>399999999800003</cbc:CompanyID>`,
		`<cbc:ActualDeliveryDate>2022-09-07</cbc:ActualDeliveryDate>`,
		`<cbc:PaymentMeansCode>10</cbc:PaymentMeansCode>`,
		`<cbc:TaxAmount currencyID="SAR">0.60</cbc:TaxAmount>`,
		`<cbc:PayableAmount currencyID="SAR">4.60</cbc:PayableAmount>`,
		`<cbc:InvoicedQuantity unitCode="PCE">2.000000</cbc:InvoicedQuantity>`,
		`<cbc:Name>قلم رصاص</cbc:Name>`,
		`<cbc:PriceAmount currencyID="SAR">2.00</cbc:PriceAmount>`,
		`<cbc:ID>ICV</cbc:ID>`,
		`<cbc:UUID>23</cbc:UUID>`,
		`<cbc:ID>PIH</cbc:ID>`,
		`<cbc:ID>QR</cbc:ID>`,
		`urn:oasis:names:specification:ubl:signature:Invoice`,
	}

	for _, check := range checks {
		if !strings.Contains(xml, check) {
			t.Errorf("XML missing: %s", check)
		}
	}
}

func TestToXMLSimplified(t *testing.T) {
	inv := sampleInvoice()
	inv.SubType = SubTypeSimplified
	inv.Customer.VAT = "" // not required for B2C

	xml, err := inv.ToXML()
	if err != nil {
		t.Fatalf("ToXML() error: %v", err)
	}

	if !strings.Contains(xml, `name="0200000"`) {
		t.Error("should contain simplified subtype")
	}
}

func TestToXMLCreditNote(t *testing.T) {
	inv := sampleInvoice()
	inv.TypeCode = TypeCreditNote
	inv.BillingReferenceID = "SME00001"

	xml, err := inv.ToXML()
	if err != nil {
		t.Fatalf("ToXML() error: %v", err)
	}

	if !strings.Contains(xml, ">381<") {
		t.Error("should contain credit note type code 381")
	}
	if !strings.Contains(xml, "SME00001") {
		t.Error("should contain billing reference")
	}
}

func TestToXMLValidation(t *testing.T) {
	// Missing required fields
	inv := &Invoice{}
	_, err := inv.ToXML()
	if err == nil {
		t.Error("expected validation error for empty invoice")
	}
}

func TestLineItemCalc(t *testing.T) {
	l := LineItem{
		Quantity:    2,
		UnitPrice:   2.00,
		TaxCategory: TaxStandard,
		TaxPercent:  15.00,
	}
	if l.NetAmount() != 4.00 {
		t.Errorf("NetAmount() = %f, want 4.00", l.NetAmount())
	}
	if l.TaxAmount() != 0.60 {
		t.Errorf("TaxAmount() = %f, want 0.60", l.TaxAmount())
	}
	if l.TotalWithTax() != 4.60 {
		t.Errorf("TotalWithTax() = %f, want 4.60", l.TotalWithTax())
	}
}

func TestMultiLineInvoice(t *testing.T) {
	inv := sampleInvoice()
	inv.Lines = []LineItem{
		{Name: "Book", Quantity: 33, UnitPrice: 3.00, TaxCategory: TaxStandard, TaxPercent: 15.00},
		{Name: "Pen", Quantity: 3, UnitPrice: 34.00, TaxCategory: TaxStandard, TaxPercent: 15.00},
	}
	// Total: 99 + 102 = 201, Tax: 14.85 + 15.30 = 30.15, With tax: 231.15

	if inv.TaxableAmount() != 201.00 {
		t.Errorf("TaxableAmount() = %f, want 201.00", inv.TaxableAmount())
	}
	if inv.TotalTax() != 30.15 {
		t.Errorf("TotalTax() = %f, want 30.15", inv.TotalTax())
	}
	if inv.TotalWithTax() != 231.15 {
		t.Errorf("TotalWithTax() = %f, want 231.15", inv.TotalWithTax())
	}

	xml, err := inv.ToXML()
	if err != nil {
		t.Fatalf("ToXML() error: %v", err)
	}
	if !strings.Contains(xml, `<cbc:PayableAmount currencyID="SAR">231.15</cbc:PayableAmount>`) {
		t.Error("payable amount should be 231.15")
	}
}
