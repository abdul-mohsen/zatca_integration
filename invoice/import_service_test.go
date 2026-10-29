package invoice

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const serviceFixture = `<?xml version="1.0" encoding="UTF-8"?>
<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"
 xmlns:cac="urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
 xmlns:cbc="urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2">
  <cbc:ID>INV-T030-1</cbc:ID>
  <cbc:UUID>550e8400-e29b-41d4-a716-446655440000</cbc:UUID>
  <cbc:IssueDate>2026-10-29</cbc:IssueDate>
  <cbc:InvoiceTypeCode name="0100000">388</cbc:InvoiceTypeCode>
  <cbc:DocumentCurrencyCode>SAR</cbc:DocumentCurrencyCode>
  <cac:AccountingSupplierParty><cac:Party>
    <cac:PartyTaxScheme><cbc:CompanyID>399999999900003</cbc:CompanyID></cac:PartyTaxScheme>
    <cac:PartyLegalEntity><cbc:RegistrationName>Supplier</cbc:RegistrationName></cac:PartyLegalEntity>
  </cac:Party></cac:AccountingSupplierParty>
  <cac:AccountingCustomerParty><cac:Party>
    <cac:PartyLegalEntity><cbc:RegistrationName>Customer</cbc:RegistrationName></cac:PartyLegalEntity>
  </cac:Party></cac:AccountingCustomerParty>
  <cac:LegalMonetaryTotal><cbc:PayableAmount currencyID="SAR">115.00</cbc:PayableAmount></cac:LegalMonetaryTotal>
  <cac:InvoiceLine>
    <cbc:ID>1</cbc:ID><cbc:InvoicedQuantity unitCode="PCE">1</cbc:InvoicedQuantity>
    <cac:Item><cbc:Name>Item</cbc:Name></cac:Item>
    <cac:Price><cbc:PriceAmount currencyID="SAR">100.00</cbc:PriceAmount></cac:Price>
  </cac:InvoiceLine>
</Invoice>`

func TestImportServiceValidPreservesBytesAndProvenance(t *testing.T) {
	input := []byte(serviceFixture)
	original := append([]byte(nil), input...)

	service := NewImportService(ImportOptions{})
	result, err := service.Import(ImportRequest{
		Bytes:            input,
		MIMEType:         "application/xml",
		OriginalName:     "supplier-invoice.xml",
		ContentReference: "upload:t030:valid",
		Provenance:       "supplier_upload",
	})
	if err != nil {
		t.Fatalf("Import() error: %v", err)
	}
	if result == nil || result.Canonical == nil {
		t.Fatal("Import() returned no canonical result")
	}
	if result.FailureClass != FailureClassTrustUnavailable {
		t.Fatalf("valid unsigned result failure class = %q, want %q",
			result.FailureClass, FailureClassTrustUnavailable)
	}
	if !bytes.Equal(result.OriginalBytes, original) {
		t.Fatal("original bytes were not retained")
	}
	input[0] = 'X'
	if !bytes.Equal(result.OriginalBytes, original) {
		t.Fatal("result retained caller-owned byte slice")
	}
	if result.Source.ContentHash == "" || result.Source.Size != int64(len(original)) {
		t.Fatalf("source evidence = %+v", result.Source)
	}
	if result.Source.MIMEType != "application/xml" ||
		result.Source.OriginalName != "supplier-invoice.xml" ||
		result.Source.ContentReference != "upload:t030:valid" ||
		result.Source.Provenance != "supplier_upload" {
		t.Fatalf("provenance = %+v", result.Source)
	}
	if result.Canonical.Invoice.Number != "INV-T030-1" ||
		result.Canonical.Invoice.Supplier.VATNumber != "399999999900003" ||
		result.Canonical.Invoice.Totals.Payable != "115.00" {
		t.Fatalf("canonical invoice = %+v", result.Canonical.Invoice)
	}
}

func TestImportServiceTamperedSignatureHasExplicitFailureClass(t *testing.T) {
	tampered := strings.Replace(serviceFixture, "</Invoice>", `
  <ext:UBLExtensions xmlns:ext="urn:oasis:names:specification:ubl:schema:xsd:CommonExtensionComponents-2">
    <ext:UBLExtension><ext:ExtensionContent>
      <ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#">
        <ds:SignatureValue>tampered</ds:SignatureValue>
        <ds:KeyInfo><ds:X509Data><ds:X509Certificate>not-a-certificate</ds:X509Certificate></ds:X509Data></ds:KeyInfo>
      </ds:Signature>
    </ext:ExtensionContent></ext:UBLExtension>
  </ext:UBLExtensions>
</Invoice>`, 1)

	service := NewImportService(ImportOptions{})
	result, err := service.Import(ImportRequest{Bytes: []byte(tampered)})
	if err == nil {
		t.Fatal("Import() accepted a tampered signature")
	}
	var failure *ImportFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %T %v, want *ImportFailure", err, err)
	}
	if failure.Class != FailureClassSignature {
		t.Fatalf("failure class = %q, want %q", failure.Class, FailureClassSignature)
	}
	if !errors.Is(err, ErrImportSignature) {
		t.Fatalf("error = %v, want ErrImportSignature", err)
	}
	if result == nil || result.Canonical == nil ||
		result.Canonical.Signature.Status == SignatureStatusVerified {
		t.Fatalf("tampered result = %+v", result)
	}
}

func TestImportServiceUnsupportedUBLHasExplicitFailureAndEvidence(t *testing.T) {
	unsupported := strings.Replace(serviceFixture, "</Invoice>", `
  <cac:AdditionalDocumentReference>
    <cbc:ID>SUPPLIER-EXTENSION</cbc:ID>
    <cbc:DocumentDescription>retain this unsupported reference</cbc:DocumentDescription>
  </cac:AdditionalDocumentReference>
</Invoice>`, 1)

	service := NewImportService(ImportOptions{})
	result, err := service.Import(ImportRequest{
		Data:       []byte(unsupported),
		Provenance: "supplier_upload",
	})
	if err == nil {
		t.Fatal("Import() accepted unsupported UBL without a classified outcome")
	}
	var failure *ImportFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %T %v, want *ImportFailure", err, err)
	}
	if failure.Class != FailureClassUnsupported {
		t.Fatalf("failure class = %q, want %q", failure.Class, FailureClassUnsupported)
	}
	if result == nil || result.Canonical == nil ||
		len(result.Canonical.UnsupportedFields) != 1 {
		t.Fatalf("unsupported result = %+v", result)
	}
	if !strings.Contains(result.Canonical.UnsupportedFields[0].RawXML, "SUPPLIER-EXTENSION") {
		t.Fatalf("unsupported evidence = %+v", result.Canonical.UnsupportedFields)
	}
}

func TestImportServiceMissingFiscalFieldHasExplicitFailureClass(t *testing.T) {
	missing := strings.Replace(serviceFixture, "<cbc:DocumentCurrencyCode>SAR</cbc:DocumentCurrencyCode>", "", 1)

	result, err := NewImportService(ImportOptions{}).Import(ImportRequest{Bytes: []byte(missing)})
	if err == nil {
		t.Fatal("Import() accepted a document without currency")
	}
	var failure *ImportFailure
	if !errors.As(err, &failure) || failure.Class != FailureClassRequiredFields {
		t.Fatalf("error = %T %v, want required_fields failure", err, err)
	}
	if !errors.Is(err, ErrImportRequiredFields) {
		t.Fatalf("error = %v, want ErrImportRequiredFields", err)
	}
	if result == nil || result.Canonical == nil {
		t.Fatal("required-field failure lost canonical evidence")
	}
}
