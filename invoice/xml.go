package invoice

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// XML namespaces used in UBL 2.1 invoices.
const (
	nsInvoice = "urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"
	nsCAC     = "urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
	nsCBC     = "urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2"
	nsEXT     = "urn:oasis:names:specification:ubl:schema:xsd:CommonExtensionComponents-2"
	nsSIG     = "urn:oasis:names:specification:ubl:schema:xsd:CommonSignatureComponents-2"
	nsSAC     = "urn:oasis:names:specification:ubl:schema:xsd:SignatureAggregateComponents-2"
	nsSBC     = "urn:oasis:names:specification:ubl:schema:xsd:SignatureBasicComponents-2"
	nsDS      = "http://www.w3.org/2000/09/xmldsig#"
	nsXADES   = "http://uri.etsi.org/01903/v1.3.2#"
)

// ToXML generates the UBL 2.1 XML representation of the invoice.
// The generated XML includes placeholder UBLExtensions for signing.
func (inv *Invoice) ToXML() (string, error) {
	if err := inv.validate(); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(fmt.Sprintf(`<Invoice xmlns="%s" xmlns:cac="%s" xmlns:cbc="%s" xmlns:ext="%s">`,
		nsInvoice, nsCAC, nsCBC, nsEXT))

	// UBL Extensions (signature placeholder)
	inv.writeUBLExtensions(&b)

	// Profile ID
	writeElement(&b, "cbc:ProfileID", inv.profileID())

	// Invoice ID & UUID
	writeElement(&b, "cbc:ID", inv.ID)
	writeElement(&b, "cbc:UUID", inv.UUID)

	// Issue Date & Time
	writeElement(&b, "cbc:IssueDate", inv.dateStr())
	writeElement(&b, "cbc:IssueTime", inv.timeStr())

	// Invoice Type Code
	b.WriteString(fmt.Sprintf(`    <cbc:InvoiceTypeCode name="%s">%d</cbc:InvoiceTypeCode>`+"\n",
		inv.SubType, inv.TypeCode))

	// Note (optional)
	if inv.Note != "" {
		lang := inv.NoteLang
		if lang == "" {
			lang = "ar"
		}
		b.WriteString(fmt.Sprintf(`    <cbc:Note languageID="%s">%s</cbc:Note>`+"\n",
			xmlEscape(lang), xmlEscape(inv.Note)))
	}

	// Currency codes
	writeElement(&b, "cbc:DocumentCurrencyCode", orDefault(inv.DocumentCurrency, "SAR"))
	writeElement(&b, "cbc:TaxCurrencyCode", orDefault(inv.TaxCurrency, "SAR"))

	// Billing Reference (for credit/debit notes)
	if inv.BillingReferenceID != "" {
		b.WriteString("    <cac:BillingReference>\n")
		b.WriteString("        <cac:InvoiceDocumentReference>\n")
		writeElementIndent(&b, "            ", "cbc:ID", inv.BillingReferenceID)
		b.WriteString("        </cac:InvoiceDocumentReference>\n")
		b.WriteString("    </cac:BillingReference>\n")
	}

	// Additional Document References
	inv.writeAdditionalDocRefs(&b)

	// Signature reference
	b.WriteString(`    <cac:Signature>` + "\n")
	writeElementIndent(&b, "      ", "cbc:ID", "urn:oasis:names:specification:ubl:signature:Invoice")
	writeElementIndent(&b, "      ", "cbc:SignatureMethod", "urn:oasis:names:specification:ubl:dsig:enveloped:xades")
	b.WriteString("    </cac:Signature>\n")

	// Supplier party
	inv.writeSupplier(&b)

	// Customer party
	inv.writeCustomer(&b)

	// Delivery
	if inv.IsStandard() || !inv.DeliveryDate.IsZero() {
		b.WriteString("    <cac:Delivery>\n")
		writeElementIndent(&b, "        ", "cbc:ActualDeliveryDate", inv.deliveryDateStr())
		b.WriteString("    </cac:Delivery>\n")
	}

	// Payment means
	b.WriteString("    <cac:PaymentMeans>\n")
	writeElementIndent(&b, "        ", "cbc:PaymentMeansCode", orDefault(inv.PaymentMeans, PaymentCash))
	if inv.InstructionNote != "" {
		writeElementIndent(&b, "        ", "cbc:InstructionNote", inv.InstructionNote)
	}
	b.WriteString("    </cac:PaymentMeans>\n")

	// Document-level allowance (discount) — only if there's a discount
	if inv.Discount > 0 {
		inv.writeAllowanceCharge(&b)
	}

	// Tax totals
	inv.writeTaxTotals(&b)

	// Legal monetary total
	inv.writeLegalMonetaryTotal(&b)

	// Invoice lines
	for i := range inv.Lines {
		inv.writeLine(&b, &inv.Lines[i], i+1)
	}

	b.WriteString("</Invoice>")

	return b.String(), nil
}

func (inv *Invoice) validate() error {
	if inv.ID == "" {
		return fmt.Errorf("invoice ID is required")
	}
	if inv.UUID == "" {
		return fmt.Errorf("invoice UUID is required")
	}
	if len(inv.Lines) == 0 {
		return fmt.Errorf("invoice must have at least one line item")
	}
	if inv.Supplier.VAT == "" {
		return fmt.Errorf("supplier VAT number is required")
	}
	if inv.IsStandard() && inv.Customer.VAT == "" {
		return fmt.Errorf("customer VAT number is required for standard invoices")
	}
	if inv.PreviousInvoiceHash == "" {
		return fmt.Errorf("previous invoice hash (PIH) is required")
	}
	return nil
}

func (inv *Invoice) writeUBLExtensions(b *strings.Builder) {
	b.WriteString("<ext:UBLExtensions>\n")
	b.WriteString("    <ext:UBLExtension>\n")
	b.WriteString("        <ext:ExtensionURI>urn:oasis:names:specification:ubl:dsig:enveloped:xades</ext:ExtensionURI>\n")
	b.WriteString("        <ext:ExtensionContent>\n")
	b.WriteString(fmt.Sprintf(`            <sig:UBLDocumentSignatures xmlns:sig="%s" xmlns:sac="%s" xmlns:sbc="%s">`+"\n", nsSIG, nsSAC, nsSBC))
	b.WriteString("                <sac:SignatureInformation>\n")
	b.WriteString("                    <cbc:ID>urn:oasis:names:specification:ubl:signature:1</cbc:ID>\n")
	b.WriteString("                    <sbc:ReferencedSignatureID>urn:oasis:names:specification:ubl:signature:Invoice</sbc:ReferencedSignatureID>\n")
	// Signature placeholder — will be filled by signing
	b.WriteString("                </sac:SignatureInformation>\n")
	b.WriteString("            </sig:UBLDocumentSignatures>\n")
	b.WriteString("        </ext:ExtensionContent>\n")
	b.WriteString("    </ext:UBLExtension>\n")
	b.WriteString("</ext:UBLExtensions>\n")
}

func (inv *Invoice) writeAdditionalDocRefs(b *strings.Builder) {
	// ICV (Invoice Counter Value)
	b.WriteString("    <cac:AdditionalDocumentReference>\n")
	writeElementIndent(b, "        ", "cbc:ID", "ICV")
	writeElementIndent(b, "        ", "cbc:UUID", fmt.Sprintf("%d", inv.InvoiceCounterValue))
	b.WriteString("    </cac:AdditionalDocumentReference>\n")

	// PIH (Previous Invoice Hash)
	b.WriteString("    <cac:AdditionalDocumentReference>\n")
	writeElementIndent(b, "        ", "cbc:ID", "PIH")
	b.WriteString("        <cac:Attachment>\n")
	b.WriteString(fmt.Sprintf(`            <cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain">%s</cbc:EmbeddedDocumentBinaryObject>`+"\n",
		inv.PreviousInvoiceHash))
	b.WriteString("        </cac:Attachment>\n")
	b.WriteString("    </cac:AdditionalDocumentReference>\n")

	// QR placeholder
	b.WriteString("    <cac:AdditionalDocumentReference>\n")
	writeElementIndent(b, "        ", "cbc:ID", "QR")
	b.WriteString("        <cac:Attachment>\n")
	b.WriteString(`            <cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain"></cbc:EmbeddedDocumentBinaryObject>` + "\n")
	b.WriteString("        </cac:Attachment>\n")
	b.WriteString("    </cac:AdditionalDocumentReference>\n")
}

func (inv *Invoice) writeSupplier(b *strings.Builder) {
	b.WriteString("    <cac:AccountingSupplierParty>\n")
	b.WriteString("        <cac:Party>\n")

	if inv.Supplier.ID != "" {
		b.WriteString("            <cac:PartyIdentification>\n")
		b.WriteString(fmt.Sprintf(`                <cbc:ID schemeID="%s">%s</cbc:ID>`+"\n",
			xmlEscape(orDefault(inv.Supplier.SchemeID, "CRN")), xmlEscape(inv.Supplier.ID)))
		b.WriteString("            </cac:PartyIdentification>\n")
	}

	inv.writeAddress(b, "            ", inv.Supplier)

	b.WriteString("            <cac:PartyTaxScheme>\n")
	writeElementIndent(b, "                ", "cbc:CompanyID", inv.Supplier.VAT)
	b.WriteString("                <cac:TaxScheme>\n")
	writeElementIndent(b, "                    ", "cbc:ID", "VAT")
	b.WriteString("                </cac:TaxScheme>\n")
	b.WriteString("            </cac:PartyTaxScheme>\n")

	b.WriteString("            <cac:PartyLegalEntity>\n")
	regName := inv.Supplier.RegistrationName
	if inv.Supplier.RegistrationNameAR != "" {
		regName = inv.Supplier.RegistrationNameAR + " | " + inv.Supplier.RegistrationName
	}
	writeElementIndent(b, "                ", "cbc:RegistrationName", regName)
	b.WriteString("            </cac:PartyLegalEntity>\n")

	b.WriteString("        </cac:Party>\n")
	b.WriteString("    </cac:AccountingSupplierParty>\n")
}

func (inv *Invoice) writeCustomer(b *strings.Builder) {
	// AccountingCustomerParty is required by the UBL 2.1 Invoice schema
	// (cardinality 1..1 in cac:Invoice). Omitting it produces:
	//   cvc-complex-type.2.4.a: Invalid content was found starting with
	//   element 'cac:PaymentMeans'. One of '{...AccountingCustomerParty}'
	//   is expected.
	// The ZATCA SDK's shipped Samples/Simplified/Invoice/Simplified_Invoice.xml
	// also includes a full ACP block, so we emit it unconditionally.
	b.WriteString("    <cac:AccountingCustomerParty>\n")
	b.WriteString("        <cac:Party>\n")

	inv.writeAddress(b, "            ", inv.Customer)

	if inv.Customer.VAT != "" {
		b.WriteString("            <cac:PartyTaxScheme>\n")
		writeElementIndent(b, "                ", "cbc:CompanyID", inv.Customer.VAT)
		b.WriteString("                <cac:TaxScheme>\n")
		writeElementIndent(b, "                    ", "cbc:ID", "VAT")
		b.WriteString("                </cac:TaxScheme>\n")
		b.WriteString("            </cac:PartyTaxScheme>\n")
	}

	if inv.Customer.RegistrationName != "" {
		b.WriteString("            <cac:PartyLegalEntity>\n")
		regName := inv.Customer.RegistrationName
		if inv.Customer.RegistrationNameAR != "" {
			regName = inv.Customer.RegistrationNameAR + " | " + inv.Customer.RegistrationName
		}
		writeElementIndent(b, "                ", "cbc:RegistrationName", regName)
		b.WriteString("            </cac:PartyLegalEntity>\n")
	}

	b.WriteString("        </cac:Party>\n")
	b.WriteString("    </cac:AccountingCustomerParty>\n")
}

func (inv *Invoice) writeAddress(b *strings.Builder, indent string, p Party) {
	b.WriteString(indent + "<cac:PostalAddress>\n")
	writeElementIndent(b, indent+"    ", "cbc:StreetName", p.Street)
	if p.Building != "" {
		writeElementIndent(b, indent+"    ", "cbc:BuildingNumber", p.Building)
	}
	if p.District != "" {
		writeElementIndent(b, indent+"    ", "cbc:CitySubdivisionName", p.District)
	}
	writeElementIndent(b, indent+"    ", "cbc:CityName", p.City)
	writeElementIndent(b, indent+"    ", "cbc:PostalZone", p.PostalCode)
	b.WriteString(indent + "    <cac:Country>\n")
	writeElementIndent(b, indent+"        ", "cbc:IdentificationCode", orDefault(p.Country, "SA"))
	b.WriteString(indent + "    </cac:Country>\n")
	b.WriteString(indent + "</cac:PostalAddress>\n")
}

func (inv *Invoice) writeAllowanceCharge(b *strings.Builder) {
	b.WriteString("    <cac:AllowanceCharge>\n")
	writeElementIndent(b, "        ", "cbc:ChargeIndicator", "false")
	writeElementIndent(b, "        ", "cbc:AllowanceChargeReason", "discount")
	b.WriteString(fmt.Sprintf(`        <cbc:Amount currencyID="%s">%s</cbc:Amount>`+"\n",
		orDefault(inv.DocumentCurrency, "SAR"), fmtAmount(inv.Discount)))

	taxCat := inv.DiscountTax
	if taxCat == "" {
		taxCat = TaxStandard
	}
	taxPct := inv.DiscountTaxPercent
	if taxPct == 0 && taxCat == TaxStandard {
		taxPct = 15.00
	}

	b.WriteString("        <cac:TaxCategory>\n")
	b.WriteString(fmt.Sprintf(`            <cbc:ID schemeID="UN/ECE 5305" schemeAgencyID="6">%s</cbc:ID>`+"\n", taxCat))
	b.WriteString(fmt.Sprintf(`            <cbc:Percent>%s</cbc:Percent>`+"\n", fmtAmount(taxPct)))
	b.WriteString("            <cac:TaxScheme>\n")
	b.WriteString(`                <cbc:ID schemeID="UN/ECE 5153" schemeAgencyID="6">VAT</cbc:ID>` + "\n")
	b.WriteString("            </cac:TaxScheme>\n")
	b.WriteString("        </cac:TaxCategory>\n")
	b.WriteString("    </cac:AllowanceCharge>\n")
}

func (inv *Invoice) writeTaxTotals(b *strings.Builder) {
	cur := orDefault(inv.DocumentCurrency, "SAR")
	totalTax := inv.TotalTax()

	// First TaxTotal (summary)
	b.WriteString("    <cac:TaxTotal>\n")
	b.WriteString(fmt.Sprintf(`        <cbc:TaxAmount currencyID="%s">%s</cbc:TaxAmount>`+"\n",
		cur, fmtAmount(totalTax)))
	b.WriteString("    </cac:TaxTotal>\n")

	// Second TaxTotal (with subtotals by category)
	b.WriteString("    <cac:TaxTotal>\n")
	b.WriteString(fmt.Sprintf(`        <cbc:TaxAmount currencyID="%s">%s</cbc:TaxAmount>`+"\n",
		cur, fmtAmount(totalTax)))

	// Group by tax category
	groups := inv.taxGroups()
	for _, g := range groups {
		b.WriteString("        <cac:TaxSubtotal>\n")
		b.WriteString(fmt.Sprintf(`            <cbc:TaxableAmount currencyID="%s">%s</cbc:TaxableAmount>`+"\n",
			cur, fmtAmount(g.taxable)))
		b.WriteString(fmt.Sprintf(`            <cbc:TaxAmount currencyID="%s">%s</cbc:TaxAmount>`+"\n",
			cur, fmtAmount(g.tax)))
		b.WriteString("            <cac:TaxCategory>\n")
		b.WriteString(fmt.Sprintf(`                <cbc:ID schemeID="UN/ECE 5305" schemeAgencyID="6">%s</cbc:ID>`+"\n", g.category))
		b.WriteString(fmt.Sprintf(`                <cbc:Percent>%s</cbc:Percent>`+"\n", fmtAmount(g.percent)))
		b.WriteString("                <cac:TaxScheme>\n")
		b.WriteString(`                    <cbc:ID schemeID="UN/ECE 5153" schemeAgencyID="6">VAT</cbc:ID>` + "\n")
		b.WriteString("                </cac:TaxScheme>\n")
		b.WriteString("            </cac:TaxCategory>\n")
		b.WriteString("        </cac:TaxSubtotal>\n")
	}
	b.WriteString("    </cac:TaxTotal>\n")
}

type taxGroup struct {
	category TaxCategory
	percent  float64
	taxable  float64
	tax      float64
}

func (inv *Invoice) taxGroups() []taxGroup {
	m := make(map[string]*taxGroup)
	var keys []string

	for _, l := range inv.Lines {
		key := fmt.Sprintf("%s-%.2f", l.TaxCategory, l.TaxPercent)
		if _, ok := m[key]; !ok {
			m[key] = &taxGroup{category: l.TaxCategory, percent: l.TaxPercent}
			keys = append(keys, key)
		}
		m[key].taxable += l.NetAmount()
		m[key].tax += l.TaxAmount()
	}

	var result []taxGroup
	for _, k := range keys {
		g := m[k]
		g.taxable = roundTo2(g.taxable)
		g.tax = roundTo2(g.tax)
		result = append(result, *g)
	}
	return result
}

func (inv *Invoice) writeLegalMonetaryTotal(b *strings.Builder) {
	cur := orDefault(inv.DocumentCurrency, "SAR")
	lineExt := 0.0
	for _, l := range inv.Lines {
		lineExt += l.NetAmount()
	}
	lineExt = roundTo2(lineExt)
	taxExcl := roundTo2(lineExt - inv.Discount)
	taxIncl := roundTo2(taxExcl + inv.TotalTax())

	b.WriteString("    <cac:LegalMonetaryTotal>\n")
	b.WriteString(fmt.Sprintf(`        <cbc:LineExtensionAmount currencyID="%s">%s</cbc:LineExtensionAmount>`+"\n", cur, fmtAmount(lineExt)))
	b.WriteString(fmt.Sprintf(`        <cbc:TaxExclusiveAmount currencyID="%s">%s</cbc:TaxExclusiveAmount>`+"\n", cur, fmtAmount(taxExcl)))
	b.WriteString(fmt.Sprintf(`        <cbc:TaxInclusiveAmount currencyID="%s">%s</cbc:TaxInclusiveAmount>`+"\n", cur, fmtAmount(taxIncl)))
	b.WriteString(fmt.Sprintf(`        <cbc:AllowanceTotalAmount currencyID="%s">%s</cbc:AllowanceTotalAmount>`+"\n", cur, fmtAmount(inv.Discount)))
	b.WriteString(fmt.Sprintf(`        <cbc:PrepaidAmount currencyID="%s">0.00</cbc:PrepaidAmount>`+"\n", cur))
	b.WriteString(fmt.Sprintf(`        <cbc:PayableAmount currencyID="%s">%s</cbc:PayableAmount>`+"\n", cur, fmtAmount(taxIncl)))
	b.WriteString("    </cac:LegalMonetaryTotal>\n")
}

func (inv *Invoice) writeLine(b *strings.Builder, line *LineItem, seq int) {
	cur := orDefault(inv.DocumentCurrency, "SAR")
	lineID := line.ID
	if lineID == "" {
		lineID = fmt.Sprintf("%d", seq)
	}

	b.WriteString("    <cac:InvoiceLine>\n")
	writeElementIndent(b, "        ", "cbc:ID", lineID)
	b.WriteString(fmt.Sprintf(`        <cbc:InvoicedQuantity unitCode="%s">%f</cbc:InvoicedQuantity>`+"\n",
		orDefault(line.UnitCode, "PCE"), line.Quantity))
	b.WriteString(fmt.Sprintf(`        <cbc:LineExtensionAmount currencyID="%s">%s</cbc:LineExtensionAmount>`+"\n",
		cur, fmtAmount(line.NetAmount())))

	// Line tax total
	b.WriteString("        <cac:TaxTotal>\n")
	b.WriteString(fmt.Sprintf(`            <cbc:TaxAmount currencyID="%s">%s</cbc:TaxAmount>`+"\n",
		cur, fmtAmount(line.TaxAmount())))
	b.WriteString(fmt.Sprintf(`            <cbc:RoundingAmount currencyID="%s">%s</cbc:RoundingAmount>`+"\n",
		cur, fmtAmount(line.TotalWithTax())))
	b.WriteString("        </cac:TaxTotal>\n")

	// Item
	b.WriteString("        <cac:Item>\n")
	writeElementIndent(b, "            ", "cbc:Name", line.Name)
	b.WriteString("            <cac:ClassifiedTaxCategory>\n")
	writeElementIndent(b, "                ", "cbc:ID", string(line.TaxCategory))
	b.WriteString(fmt.Sprintf(`                <cbc:Percent>%s</cbc:Percent>`+"\n", fmtAmount(line.TaxPercent)))
	b.WriteString("                <cac:TaxScheme>\n")
	writeElementIndent(b, "                    ", "cbc:ID", "VAT")
	b.WriteString("                </cac:TaxScheme>\n")
	b.WriteString("            </cac:ClassifiedTaxCategory>\n")
	b.WriteString("        </cac:Item>\n")

	// Price
	b.WriteString("        <cac:Price>\n")
	b.WriteString(fmt.Sprintf(`            <cbc:PriceAmount currencyID="%s">%s</cbc:PriceAmount>`+"\n",
		cur, fmtAmount(line.UnitPrice)))
	if line.Discount > 0 {
		b.WriteString("            <cac:AllowanceCharge>\n")
		writeElementIndent(b, "                ", "cbc:ChargeIndicator", "false")
		writeElementIndent(b, "                ", "cbc:AllowanceChargeReason", "discount")
		b.WriteString(fmt.Sprintf(`                <cbc:Amount currencyID="%s">%s</cbc:Amount>`+"\n",
			cur, fmtAmount(line.Discount)))
		b.WriteString("            </cac:AllowanceCharge>\n")
	}
	b.WriteString("        </cac:Price>\n")

	b.WriteString("    </cac:InvoiceLine>\n")
}

func writeElement(b *strings.Builder, tag, value string) {
	writeElementIndent(b, "    ", tag, value)
}

func writeElementIndent(b *strings.Builder, indent, tag, value string) {
	b.WriteString(fmt.Sprintf("%s<%s>%s</%s>\n", indent, tag, xmlEscape(value), tag))
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
