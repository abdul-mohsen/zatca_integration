package invoice

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

const (
	UBLImportContract = "afrita.zatca.ubl-import"
	UBLImportVersion  = "1"
	MaxUBLImportBytes = 5 << 20
	MaxUBLImportDepth = 256
	MaxUBLImportToken = 200000

	SchemaStatusValid       = "schema_valid"
	SchemaStatusInvalid     = "schema_invalid"
	SchemaStatusUnavailable = "schema_unavailable"

	SignatureStatusVerified   = "verified"
	SignatureStatusUnverified = "unverified"
	SignatureStatusUnsigned   = "unsigned"
	SignatureStatusInvalid    = "invalid"
)

var (
	ErrUBLImportOversized = errors.New("ubl import XML exceeds the configured size limit")
	ErrUBLImportUnsafe    = errors.New("ubl import XML contains a prohibited DTD or external entity")
	ErrUBLImportMalformed = errors.New("ubl import XML is malformed")
)

// ImportOptions controls optional repository-provided schema and trust
// validation. The parser never treats structural checks as cryptographic
// verification when no trusted certificate roots are supplied.
type ImportOptions struct {
	TrustedCertificates []*x509.Certificate
	SchemaValidator     func([]byte) []ValidationIssue
}

type XMLSourceEvidence struct {
	ContentHash      string `json:"content_hash"`
	Size             int64  `json:"size"`
	MIMEType         string `json:"mime_type,omitempty"`
	OriginalName     string `json:"original_name,omitempty"`
	ContentReference string `json:"content_reference,omitempty"`
	Provenance       string `json:"provenance"`
}

type ValidationIssue struct {
	Code     string `json:"code"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

type ValidationState struct {
	WellFormed      bool              `json:"well_formed"`
	SchemaStatus    string            `json:"schema_status"`
	SignatureStatus string            `json:"signature_status"`
	ReviewRequired  bool              `json:"review_required"`
	Issues          []ValidationIssue `json:"issues,omitempty"`
}

type SignatureEvidence struct {
	Present             bool   `json:"present"`
	Status              string `json:"status"`
	Algorithm           string `json:"algorithm,omitempty"`
	SignatureValueHash  string `json:"signature_value_hash,omitempty"`
	InvoiceDigest       string `json:"invoice_digest,omitempty"`
	CertificateSHA256   string `json:"certificate_sha256,omitempty"`
	CertificateIssuer   string `json:"certificate_issuer,omitempty"`
	CertificateSerial   string `json:"certificate_serial,omitempty"`
	CertificateSubject  string `json:"certificate_subject,omitempty"`
	VerificationMessage string `json:"verification_message,omitempty"`
}

type PartyData struct {
	Name               string `json:"name,omitempty"`
	NameAR             string `json:"name_ar,omitempty"`
	VATNumber          string `json:"vat_number,omitempty"`
	RegistrationID     string `json:"registration_id,omitempty"`
	RegistrationScheme string `json:"registration_scheme,omitempty"`
	Street             string `json:"street,omitempty"`
	Building           string `json:"building,omitempty"`
	District           string `json:"district,omitempty"`
	City               string `json:"city,omitempty"`
	PostalCode         string `json:"postal_code,omitempty"`
	Country            string `json:"country,omitempty"`
}

type LineData struct {
	ID            string `json:"id,omitempty"`
	Name          string `json:"name,omitempty"`
	Quantity      string `json:"quantity,omitempty"`
	UnitCode      string `json:"unit_code,omitempty"`
	UnitPrice     string `json:"unit_price,omitempty"`
	LineExtension string `json:"line_extension,omitempty"`
	Discount      string `json:"discount,omitempty"`
	TaxCategory   string `json:"tax_category,omitempty"`
	TaxRate       string `json:"tax_rate,omitempty"`
	TaxAmount     string `json:"tax_amount,omitempty"`
}

type AllowanceChargeData struct {
	ChargeIndicator string `json:"charge_indicator"`
	Amount          string `json:"amount,omitempty"`
	Currency        string `json:"currency,omitempty"`
	Reason          string `json:"reason,omitempty"`
	TaxCategory     string `json:"tax_category,omitempty"`
	TaxRate         string `json:"tax_rate,omitempty"`
}

type VATCategoryData struct {
	Category      string `json:"category,omitempty"`
	Rate          string `json:"rate,omitempty"`
	TaxableAmount string `json:"taxable_amount,omitempty"`
	TaxAmount     string `json:"tax_amount,omitempty"`
	Currency      string `json:"currency,omitempty"`
}

type TotalsData struct {
	LineExtension  string `json:"line_extension,omitempty"`
	TaxExclusive   string `json:"tax_exclusive,omitempty"`
	TaxInclusive   string `json:"tax_inclusive,omitempty"`
	AllowanceTotal string `json:"allowance_total,omitempty"`
	ChargeTotal    string `json:"charge_total,omitempty"`
	Prepaid        string `json:"prepaid,omitempty"`
	Payable        string `json:"payable,omitempty"`
	Currency       string `json:"currency,omitempty"`
}

type PaymentMeansData struct {
	Code            string `json:"code,omitempty"`
	InstructionNote string `json:"instruction_note,omitempty"`
}

type ParsedInvoice struct {
	Number        string                `json:"number,omitempty"`
	UUID          string                `json:"uuid,omitempty"`
	IssueDate     string                `json:"issue_date,omitempty"`
	IssueTime     string                `json:"issue_time,omitempty"`
	TypeCode      string                `json:"type_code,omitempty"`
	SubtypeCode   string                `json:"subtype_code,omitempty"`
	Currency      string                `json:"currency,omitempty"`
	TaxCurrency   string                `json:"tax_currency,omitempty"`
	Supplier      PartyData             `json:"supplier"`
	Customer      PartyData             `json:"customer"`
	Lines         []LineData            `json:"lines,omitempty"`
	Allowances    []AllowanceChargeData `json:"allowances,omitempty"`
	VATCategories []VATCategoryData     `json:"vat_categories,omitempty"`
	Totals        TotalsData            `json:"totals"`
	PaymentMeans  []PaymentMeansData    `json:"payment_means,omitempty"`
}

type UnknownExtension struct {
	Path      string `json:"path"`
	Namespace string `json:"namespace,omitempty"`
	LocalName string `json:"local_name"`
	RawXML    string `json:"raw_xml"`
}

type UnsupportedField struct {
	Path      string `json:"path"`
	Namespace string `json:"namespace,omitempty"`
	LocalName string `json:"local_name"`
	Reason    string `json:"reason"`
	RawXML    string `json:"raw_xml"`
}

type MappedField struct {
	FieldKey     string          `json:"field_key"`
	ValueText    string          `json:"value_text,omitempty"`
	ValueJSON    json.RawMessage `json:"value_json,omitempty"`
	Confidence   string          `json:"confidence"`
	EvidenceText string          `json:"evidence_text,omitempty"`
}

// UBLImport is the versioned parser-to-intake contract. It intentionally
// carries both normalized fields and raw unknown/unsupported XML so a later
// reviewer can see what the importer did not understand.
type UBLImport struct {
	Contract          string             `json:"contract"`
	Version           string             `json:"version"`
	Source            XMLSourceEvidence  `json:"source"`
	IdentityKey       string             `json:"identity_key"`
	Invoice           ParsedInvoice      `json:"invoice"`
	Signature         SignatureEvidence  `json:"signature"`
	Validation        ValidationState    `json:"validation"`
	Fields            []MappedField      `json:"fields,omitempty"`
	UnknownExtensions []UnknownExtension `json:"unknown_extensions,omitempty"`
	UnsupportedFields []UnsupportedField `json:"unsupported_fields,omitempty"`
}

// ParseSignedUBL is the public import boundary used by the ZATCA integration
// worker. It is an alias with an explicit name for callers importing supplier
// XML rather than generating an invoice.
func ParseSignedUBL(data []byte, options ImportOptions) (*UBLImport, error) {
	return ParseUBL(data, options)
}

// ParseUBL safely parses a UBL 2.1 Invoice without resolving DTDs, entities,
// network references, or local external resources.
func ParseUBL(data []byte, options ImportOptions) (*UBLImport, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty input", ErrUBLImportMalformed)
	}
	if len(data) > MaxUBLImportBytes {
		return nil, fmt.Errorf("%w: %d bytes > %d", ErrUBLImportOversized, len(data), MaxUBLImportBytes)
	}
	if containsProhibitedXMLConstruct(data) {
		return nil, ErrUBLImportUnsafe
	}

	rootName, err := scanSafeXML(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUBLImportMalformed, err)
	}
	if rootName.Local != "Invoice" || rootName.Space != nsInvoice {
		return nil, fmt.Errorf("%w: root is %s in namespace %s, want UBL Invoice", ErrUBLImportMalformed, rootName.Local, rootName.Space)
	}

	doc := etree.NewDocument()
	doc.ReadSettings = etree.ReadSettings{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		return nil, fmt.Errorf("unsupported XML charset %q", charset)
	}}
	if err := doc.ReadFromBytes(data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUBLImportMalformed, err)
	}
	root := doc.Root()
	if root == nil {
		return nil, fmt.Errorf("%w: missing root element", ErrUBLImportMalformed)
	}

	result := &UBLImport{
		Contract: UBLImportContract,
		Version:  UBLImportVersion,
		Source: XMLSourceEvidence{
			ContentHash: sha256Hex(data),
			Size:        int64(len(data)),
			Provenance:  "supplier_xml",
		},
		Validation: ValidationState{
			WellFormed:      true,
			SchemaStatus:    SchemaStatusUnavailable,
			SignatureStatus: SignatureStatusUnsigned,
			ReviewRequired:  true,
		},
	}

	result.Invoice = parseInvoice(root)
	result.Signature = parseSignature(root, options.TrustedCertificates)
	result.Validation.SignatureStatus = result.Signature.Status
	result.UnknownExtensions = parseUnknownExtensions(root)
	result.UnsupportedFields = parseUnsupportedFields(root)
	result.Fields = mappedFields(result.Invoice)
	result.IdentityKey = identityKey(result.Invoice)
	validateUBLProfile(result)

	if options.SchemaValidator != nil {
		issues := options.SchemaValidator(data)
		schemaInvalid := false
		for _, issue := range issues {
			if strings.EqualFold(strings.TrimSpace(issue.Severity), "warning") {
				continue
			}
			schemaInvalid = true
		}
		if schemaInvalid {
			result.Validation.SchemaStatus = SchemaStatusInvalid
		} else if result.Validation.SchemaStatus != SchemaStatusInvalid {
			result.Validation.SchemaStatus = SchemaStatusValid
		}
		result.Validation.Issues = append(result.Validation.Issues, issues...)
	}
	if result.Validation.SchemaStatus == SchemaStatusInvalid ||
		result.Validation.SignatureStatus != SignatureStatusVerified ||
		len(result.Validation.Issues) > 0 {
		result.Validation.ReviewRequired = true
	}
	return result, nil
}

func containsProhibitedXMLConstruct(data []byte) bool {
	upper := bytes.ToUpper(data)
	return bytes.Contains(upper, []byte("<!DOCTYPE")) ||
		bytes.Contains(upper, []byte("<!ENTITY")) ||
		bytes.Contains(upper, []byte("SYSTEM \"")) ||
		bytes.Contains(upper, []byte("SYSTEM '")) ||
		bytes.Contains(upper, []byte("PUBLIC \"")) ||
		bytes.Contains(upper, []byte("PUBLIC '"))
}

func scanSafeXML(data []byte) (xml.Name, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	decoder.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		return nil, fmt.Errorf("unsupported XML charset %q", charset)
	}
	var root xml.Name
	depth := 0
	tokens := 0
	rootClosed := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if root.Local == "" || depth != 0 || !rootClosed {
				return xml.Name{}, errors.New("missing or incomplete root element")
			}
			return root, nil
		}
		if err != nil {
			return xml.Name{}, err
		}
		tokens++
		if tokens > MaxUBLImportToken {
			return xml.Name{}, errors.New("XML token limit exceeded")
		}
		switch value := token.(type) {
		case xml.Directive:
			upper := bytes.ToUpper(bytes.TrimSpace(value))
			if bytes.Contains(upper, []byte("DOCTYPE")) || bytes.Contains(upper, []byte("ENTITY")) {
				return xml.Name{}, ErrUBLImportUnsafe
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				return xml.Name{}, errors.New("non-whitespace character data outside root element")
			}
		case xml.StartElement:
			if depth == 0 {
				if root.Local != "" {
					return xml.Name{}, errors.New("multiple root elements")
				}
				root = value.Name
			}
			depth++
			if depth > MaxUBLImportDepth {
				return xml.Name{}, errors.New("XML depth limit exceeded")
			}
		case xml.EndElement:
			depth--
			if depth < 0 {
				return xml.Name{}, errors.New("unexpected closing element")
			}
			if depth == 0 {
				rootClosed = true
			}
		}
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func parseInvoice(root *etree.Element) ParsedInvoice {
	inv := ParsedInvoice{}
	inv.Number = childText(root, nsCBC, "ID")
	inv.UUID = childText(root, nsCBC, "UUID")
	inv.IssueDate = childText(root, nsCBC, "IssueDate")
	inv.IssueTime = childText(root, nsCBC, "IssueTime")
	if issueDate := strings.TrimSpace(inv.IssueDate); issueDate != "" {
		if t, err := time.Parse("2006-01-02", issueDate); err != nil {
			// The profile validator reports the field without changing the
			// source value, preserving the supplier evidence verbatim.
			_ = t
		}
	}
	if typeCode := child(root, nsCBC, "InvoiceTypeCode"); typeCode != nil {
		inv.TypeCode = strings.TrimSpace(typeCode.Text())
		inv.SubtypeCode = strings.TrimSpace(typeCode.SelectAttrValue("name", ""))
	}
	inv.Currency = childText(root, nsCBC, "DocumentCurrencyCode")
	inv.TaxCurrency = childText(root, nsCBC, "TaxCurrencyCode")
	if supplier := child(root, nsCAC, "AccountingSupplierParty"); supplier != nil {
		inv.Supplier = parseParty(supplier)
	}
	if customer := child(root, nsCAC, "AccountingCustomerParty"); customer != nil {
		inv.Customer = parseParty(customer)
	}
	for _, line := range children(root, nsCAC, "InvoiceLine") {
		inv.Lines = append(inv.Lines, parseLine(line))
	}
	for _, allowance := range children(root, nsCAC, "AllowanceCharge") {
		inv.Allowances = append(inv.Allowances, parseAllowanceCharge(allowance))
	}
	for _, taxTotal := range children(root, nsCAC, "TaxTotal") {
		for _, subtotal := range children(taxTotal, nsCAC, "TaxSubtotal") {
			inv.VATCategories = append(inv.VATCategories, parseVATCategory(subtotal))
		}
	}
	if total := child(root, nsCAC, "LegalMonetaryTotal"); total != nil {
		inv.Totals = TotalsData{
			LineExtension:  amountText(total, "LineExtensionAmount"),
			TaxExclusive:   amountText(total, "TaxExclusiveAmount"),
			TaxInclusive:   amountText(total, "TaxInclusiveAmount"),
			AllowanceTotal: amountText(total, "AllowanceTotalAmount"),
			ChargeTotal:    amountText(total, "ChargeTotalAmount"),
			Prepaid:        amountText(total, "PrepaidAmount"),
			Payable:        amountText(total, "PayableAmount"),
			Currency:       amountCurrency(total, "PayableAmount"),
		}
	}
	for _, payment := range children(root, nsCAC, "PaymentMeans") {
		inv.PaymentMeans = append(inv.PaymentMeans, PaymentMeansData{
			Code:            childText(payment, nsCBC, "PaymentMeansCode"),
			InstructionNote: childText(payment, nsCBC, "InstructionNote"),
		})
	}
	return inv
}

func parseParty(aggregate *etree.Element) PartyData {
	party := PartyData{}
	value := child(aggregate, nsCAC, "Party")
	if value == nil {
		return party
	}
	if identification := child(value, nsCAC, "PartyIdentification"); identification != nil {
		id := child(identification, nsCBC, "ID")
		if id != nil {
			party.RegistrationID = strings.TrimSpace(id.Text())
			party.RegistrationScheme = id.SelectAttrValue("schemeID", "")
		}
	}
	if address := child(value, nsCAC, "PostalAddress"); address != nil {
		party.Street = childText(address, nsCBC, "StreetName")
		party.Building = childText(address, nsCBC, "BuildingNumber")
		party.District = childText(address, nsCBC, "CitySubdivisionName")
		party.City = childText(address, nsCBC, "CityName")
		party.PostalCode = childText(address, nsCBC, "PostalZone")
		if country := child(address, nsCAC, "Country"); country != nil {
			party.Country = childText(country, nsCBC, "IdentificationCode")
		}
	}
	if tax := child(value, nsCAC, "PartyTaxScheme"); tax != nil {
		party.VATNumber = childText(tax, nsCBC, "CompanyID")
	}
	if legal := child(value, nsCAC, "PartyLegalEntity"); legal != nil {
		for _, name := range children(legal, nsCBC, "RegistrationName") {
			setLocalizedName(&party.Name, &party.NameAR, name)
		}
	}
	if partyName := child(value, nsCAC, "PartyName"); partyName != nil {
		setLocalizedName(&party.Name, &party.NameAR, child(partyName, nsCBC, "Name"))
	}
	return party
}

func parseLine(line *etree.Element) LineData {
	value := LineData{
		ID:            childText(line, nsCBC, "ID"),
		LineExtension: amountText(line, "LineExtensionAmount"),
	}
	if quantity := child(line, nsCBC, "InvoicedQuantity"); quantity != nil {
		value.Quantity = strings.TrimSpace(quantity.Text())
		value.UnitCode = quantity.SelectAttrValue("unitCode", "")
	}
	if item := child(line, nsCAC, "Item"); item != nil {
		value.Name = childText(item, nsCBC, "Name")
		if value.Name == "" {
			value.Name = childText(item, nsCBC, "Description")
		}
		if tax := child(item, nsCAC, "ClassifiedTaxCategory"); tax != nil {
			value.TaxCategory = childText(tax, nsCBC, "ID")
			value.TaxRate = childText(tax, nsCBC, "Percent")
		}
	}
	if price := child(line, nsCAC, "Price"); price != nil {
		value.UnitPrice = amountText(price, "PriceAmount")
		for _, allowance := range children(price, nsCAC, "AllowanceCharge") {
			if strings.EqualFold(childText(allowance, nsCBC, "ChargeIndicator"), "false") {
				value.Discount = amountText(allowance, "Amount")
			}
		}
	}
	if tax := child(line, nsCAC, "TaxTotal"); tax != nil {
		value.TaxAmount = amountText(tax, "TaxAmount")
	}
	return value
}

func parseAllowanceCharge(value *etree.Element) AllowanceChargeData {
	result := AllowanceChargeData{
		ChargeIndicator: strconv.FormatBool(strings.EqualFold(childText(value, nsCBC, "ChargeIndicator"), "true")),
		Amount:          amountText(value, "Amount"),
		Currency:        amountCurrency(value, "Amount"),
		Reason:          childText(value, nsCBC, "AllowanceChargeReason"),
	}
	if category := child(value, nsCAC, "TaxCategory"); category != nil {
		result.TaxCategory = childText(category, nsCBC, "ID")
		result.TaxRate = childText(category, nsCBC, "Percent")
	}
	return result
}

func parseVATCategory(subtotal *etree.Element) VATCategoryData {
	result := VATCategoryData{
		TaxableAmount: amountText(subtotal, "TaxableAmount"),
		TaxAmount:     amountText(subtotal, "TaxAmount"),
		Currency:      amountCurrency(subtotal, "TaxAmount"),
	}
	if category := child(subtotal, nsCAC, "TaxCategory"); category != nil {
		result.Category = childText(category, nsCBC, "ID")
		result.Rate = childText(category, nsCBC, "Percent")
	}
	return result
}

func parseSignature(root *etree.Element, roots []*x509.Certificate) SignatureEvidence {
	result := SignatureEvidence{Status: SignatureStatusUnsigned}
	var signature *etree.Element
	for _, element := range descendants(root) {
		if isElement(element, nsDS, "Signature") {
			signature = element
			break
		}
	}
	if signature == nil {
		return result
	}
	result.Present = true
	result.Status = SignatureStatusUnverified
	if method := descendant(signature, nsDS, "SignatureMethod"); method != nil {
		result.Algorithm = method.SelectAttrValue("Algorithm", "")
	}
	signatureValue := descendantText(signature, nsDS, "SignatureValue")
	if signatureValue != "" {
		result.SignatureValueHash = sha256Hex([]byte(signatureValue))
	}
	for _, reference := range children(descendant(signature, nsDS, "SignedInfo"), nsDS, "Reference") {
		if result.InvoiceDigest == "" {
			result.InvoiceDigest = childText(reference, nsDS, "DigestValue")
		}
	}
	certificateValue := strings.Join(strings.Fields(descendantText(signature, nsDS, "X509Certificate")), "")
	if certificateValue == "" || signatureValue == "" {
		result.Status = SignatureStatusInvalid
		result.VerificationMessage = "signature value and X509 certificate are required"
		return result
	}
	certDER, err := decodeCertificate(certificateValue)
	if err != nil {
		result.Status = SignatureStatusInvalid
		result.VerificationMessage = err.Error()
		return result
	}
	sum := sha256.Sum256(certDER)
	result.CertificateSHA256 = hex.EncodeToString(sum[:])
	if cert, err := x509.ParseCertificate(certDER); err == nil {
		result.CertificateIssuer = cert.Issuer.String()
		result.CertificateSubject = cert.Subject.String()
		result.CertificateSerial = cert.SerialNumber.String()
	} else {
		result.Status = SignatureStatusInvalid
		result.VerificationMessage = fmt.Sprintf("invalid X509 certificate: %v", err)
		return result
	}
	if len(roots) == 0 {
		result.VerificationMessage = "certificate was parsed but no trusted certificate roots were supplied"
		return result
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromString(elementXML(root)); err != nil {
		result.Status = SignatureStatusInvalid
		result.VerificationMessage = fmt.Sprintf("signature verification parse failed: %v", err)
		return result
	}
	context := dsig.NewDefaultValidationContext(&dsig.MemoryX509CertificateStore{Roots: roots})
	if _, err := context.Validate(doc.Root()); err != nil {
		result.Status = SignatureStatusInvalid
		result.VerificationMessage = fmt.Sprintf("signature verification failed: %v", err)
		return result
	}
	result.Status = SignatureStatusVerified
	result.VerificationMessage = "signature verified against supplied certificate roots"
	return result
}

func decodeCertificate(value string) ([]byte, error) {
	data, err := hex.DecodeString(value)
	if err == nil {
		return data, nil
	}
	data, err = base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("invalid X509 certificate encoding: %w", err)
	}
	return data, nil
}

func validateUBLProfile(result *UBLImport) {
	add := func(code, field, message string) {
		result.Validation.Issues = append(result.Validation.Issues, ValidationIssue{
			Code: code, Field: field, Message: message, Severity: "error",
		})
		result.Validation.SchemaStatus = SchemaStatusInvalid
	}
	if result.Invoice.Number == "" {
		add("missing_invoice_number", "invoice.number", "cbc:ID is required")
	}
	if result.Invoice.UUID == "" {
		add("missing_uuid", "invoice.uuid", "cbc:UUID is required")
	}
	if result.Invoice.IssueDate == "" {
		add("missing_issue_date", "invoice.issue_date", "cbc:IssueDate is required")
	} else if _, err := time.Parse("2006-01-02", result.Invoice.IssueDate); err != nil {
		add("invalid_issue_date", "invoice.issue_date", "cbc:IssueDate must use YYYY-MM-DD")
	}
	if result.Invoice.Currency == "" {
		add("missing_currency", "invoice.currency", "cbc:DocumentCurrencyCode is required")
	}
	if len(result.Invoice.Lines) == 0 {
		add("missing_lines", "invoice.lines", "at least one cac:InvoiceLine is required")
	}
	if result.Invoice.Totals.Payable == "" {
		add("missing_payable_total", "invoice.totals.payable", "cac:LegalMonetaryTotal/cbc:PayableAmount is required")
	}
}

func identityKey(inv ParsedInvoice) string {
	if strings.TrimSpace(inv.UUID) != "" {
		return "uuid:" + strings.TrimSpace(inv.UUID)
	}
	number := strings.TrimSpace(inv.Number)
	issueDate := strings.TrimSpace(inv.IssueDate)
	if number == "" || issueDate == "" {
		return ""
	}
	return "invoice:" + number + ":" + issueDate
}

func mappedFields(inv ParsedInvoice) []MappedField {
	fields := make([]MappedField, 0, 16+len(inv.Lines)*8)
	addText := func(key, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		fields = append(fields, MappedField{FieldKey: key, ValueText: value, Confidence: "1"})
	}
	addJSON := func(key string, value any, evidence string) {
		data, err := json.Marshal(value)
		if err != nil {
			return
		}
		fields = append(fields, MappedField{FieldKey: key, ValueJSON: data, Confidence: "1", EvidenceText: evidence})
	}
	addText("invoice.number", inv.Number)
	addText("invoice.uuid", inv.UUID)
	addText("invoice.issue_date", inv.IssueDate)
	addText("invoice.issue_time", inv.IssueTime)
	addText("invoice.type_code", inv.TypeCode)
	addText("invoice.subtype_code", inv.SubtypeCode)
	addText("invoice.currency", inv.Currency)
	addText("invoice.tax_currency", inv.TaxCurrency)
	addText("supplier.name", inv.Supplier.Name)
	addText("supplier.name_ar", inv.Supplier.NameAR)
	addText("supplier.vat_number", inv.Supplier.VATNumber)
	addText("supplier.registration_id", inv.Supplier.RegistrationID)
	addText("customer.name", inv.Customer.Name)
	addText("customer.name_ar", inv.Customer.NameAR)
	addText("customer.vat_number", inv.Customer.VATNumber)
	addText("totals.payable", inv.Totals.Payable)
	addText("totals.tax_inclusive", inv.Totals.TaxInclusive)
	addText("totals.tax_exclusive", inv.Totals.TaxExclusive)
	addJSON("totals.vat_categories", inv.VATCategories, "UBL TaxSubtotal")
	addJSON("totals.allowances", inv.Allowances, "UBL AllowanceCharge")
	addJSON("payment_means", inv.PaymentMeans, "UBL PaymentMeans")
	for i, line := range inv.Lines {
		prefix := "line_items[" + strconv.Itoa(i) + "]."
		addText(prefix+"id", line.ID)
		addText(prefix+"name", line.Name)
		addText(prefix+"quantity", line.Quantity)
		addText(prefix+"unit_code", line.UnitCode)
		addText(prefix+"unit_price", line.UnitPrice)
		addText(prefix+"line_extension", line.LineExtension)
		addText(prefix+"discount", line.Discount)
		addText(prefix+"tax_category", line.TaxCategory)
		addText(prefix+"tax_rate", line.TaxRate)
		addText(prefix+"tax_amount", line.TaxAmount)
	}
	return fields
}

func parseUnknownExtensions(root *etree.Element) []UnknownExtension {
	var result []UnknownExtension
	if extensions := child(root, nsEXT, "UBLExtensions"); extensions != nil {
		for i, extension := range children(extensions, nsEXT, "UBLExtension") {
			result = append(result, UnknownExtension{
				Path:      fmt.Sprintf("ext:UBLExtensions/ext:UBLExtension[%d]", i+1),
				Namespace: namespaceOf(extension),
				LocalName: extension.Tag,
				RawXML:    elementXML(extension),
			})
		}
	}
	return result
}

func parseUnsupportedFields(root *etree.Element) []UnsupportedField {
	known := map[string]bool{
		"ProfileID": true, "ID": true, "UUID": true, "IssueDate": true,
		"IssueTime": true, "InvoiceTypeCode": true, "Note": true,
		"DocumentCurrencyCode": true, "TaxCurrencyCode": true,
		"AdditionalDocumentReference": true, "Signature": true,
		"AccountingSupplierParty": true, "AccountingCustomerParty": true,
		"Delivery": true, "PaymentMeans": true, "AllowanceCharge": true,
		"TaxTotal": true, "LegalMonetaryTotal": true, "InvoiceLine": true,
		"UBLExtensions": true,
	}
	var result []UnsupportedField
	for _, element := range root.ChildElements() {
		if known[element.Tag] {
			if element.Tag == "AdditionalDocumentReference" {
				id := childText(element, nsCBC, "ID")
				if id == "ICV" || id == "PIH" || id == "QR" {
					continue
				}
				result = append(result, unsupportedElement(element, "additional document reference is not supported by the import mapper"))
			}
			continue
		}
		result = append(result, unsupportedElement(element, "element is not supported by the import mapper"))
	}
	return result
}

func unsupportedElement(element *etree.Element, reason string) UnsupportedField {
	return UnsupportedField{
		Path:      "Invoice/" + element.Tag,
		Namespace: namespaceOf(element),
		LocalName: element.Tag,
		Reason:    reason,
		RawXML:    elementXML(element),
	}
}

func elementXML(element *etree.Element) string {
	if element == nil {
		return ""
	}
	doc := etree.NewDocument()
	doc.SetRoot(element.Copy())
	value, err := doc.WriteToString()
	if err != nil {
		return ""
	}
	return value
}

func descendants(root *etree.Element) []*etree.Element {
	var result []*etree.Element
	for _, child := range root.ChildElements() {
		result = append(result, child)
		result = append(result, descendants(child)...)
	}
	return result
}

func descendant(root *etree.Element, namespace, local string) *etree.Element {
	if root == nil {
		return nil
	}
	for _, element := range descendants(root) {
		if isElement(element, namespace, local) {
			return element
		}
	}
	return nil
}

func descendantText(root *etree.Element, namespace, local string) string {
	return text(descendant(root, namespace, local))
}

func children(parent *etree.Element, namespace, local string) []*etree.Element {
	var result []*etree.Element
	if parent == nil {
		return result
	}
	for _, child := range parent.ChildElements() {
		if isElement(child, namespace, local) {
			result = append(result, child)
		}
	}
	return result
}

func child(parent *etree.Element, namespace, local string) *etree.Element {
	values := children(parent, namespace, local)
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

func childText(parent *etree.Element, namespace, local string) string {
	return text(child(parent, namespace, local))
}

func amountText(parent *etree.Element, local string) string {
	return childText(parent, nsCBC, local)
}

func amountCurrency(parent *etree.Element, local string) string {
	value := child(parent, nsCBC, local)
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.SelectAttrValue("currencyID", ""))
}

func text(element *etree.Element) string {
	if element == nil {
		return ""
	}
	return strings.TrimSpace(element.Text())
}

func setLocalizedName(name, nameAR *string, element *etree.Element) {
	value := text(element)
	if value == "" {
		return
	}
	language := strings.ToLower(element.SelectAttrValue("languageID", ""))
	if strings.HasPrefix(language, "ar") || containsArabic(value) {
		if *nameAR == "" {
			*nameAR = value
		}
		return
	}
	if *name == "" {
		*name = value
	}
}

func containsArabic(value string) bool {
	for _, r := range value {
		if unicode.In(r, unicode.Arabic) {
			return true
		}
	}
	return false
}

func isElement(element *etree.Element, namespace, local string) bool {
	return element != nil && element.Tag == local && namespaceOf(element) == namespace
}

func namespaceOf(element *etree.Element) string {
	if element == nil {
		return ""
	}
	for current := element; current != nil; current = current.Parent() {
		for _, attribute := range current.Attr {
			if attribute.Space == "xmlns" && attribute.Key == element.Space {
				return attribute.Value
			}
			if attribute.Space == "" && attribute.Key == "xmlns" && element.Space == "" {
				return attribute.Value
			}
		}
	}
	return ""
}
