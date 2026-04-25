package crypto

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// SignResult holds the result of signing an invoice.
type SignResult struct {
	SignedXML    string // Complete signed invoice XML
	InvoiceHash string // Base64-encoded SHA-256 hash of the invoice
	SignatureB64 string // Base64-encoded ECDSA signature
	QRCode       string // Base64-encoded TLV QR data
}

// SignInvoice signs a ZATCA invoice XML using the given private key and certificate.
// It computes the invoice hash, builds the XAdES-BES signature, injects it into
// the UBLExtensions, and generates the QR code for simplified invoices.
func SignInvoice(unsignedXML string, privateKey *ecdsa.PrivateKey, cert *CertInfo, isSimplified bool) (*SignResult, error) {
	// Step 1: Compute invoice hash (digest of XML with 3 sections removed)
	invoiceHash, err := InvoiceHash(unsignedXML)
	if err != nil {
		return nil, fmt.Errorf("computing invoice hash: %w", err)
	}

	// Step 2: Build XAdES SignedProperties
	signingTime := time.Now().Format("2006-01-02T15:04:05")
	signedProps := buildSignedProperties(signingTime, cert)

	// Step 3: Compute SignedProperties digest
	signedPropsHash := sha256.Sum256([]byte(signedProps))
	signedPropsDigestHex := fmt.Sprintf("%x", signedPropsHash[:])
	signedPropsDigest := base64.StdEncoding.EncodeToString([]byte(signedPropsDigestHex))

	// Step 4: Build SignedInfo and compute its digest for signing
	signedInfo := buildSignedInfo(invoiceHash, signedPropsDigest)

	// Step 5: Canonicalize SignedInfo and sign with ECDSA
	signedInfoHash := sha256.Sum256([]byte(signedInfo))
	r, s, err := ecdsa.Sign(rand.Reader, privateKey, signedInfoHash[:])
	if err != nil {
		return nil, fmt.Errorf("ECDSA sign: %w", err)
	}

	// Encode signature as DER (standard ASN.1 encoding used by ZATCA)
	signatureBytes := encodeECDSASignatureDER(r, s)
	signatureB64 := base64.StdEncoding.EncodeToString(signatureBytes)

	// Step 6: Build complete ds:Signature XML
	dsSignature := buildDSSignature(signedInfo, signatureB64, cert, signedProps)

	// Step 7: Inject signature into UBLExtensions
	signedXML := injectSignature(unsignedXML, dsSignature)

	// Step 8: Generate QR code
	var qrCode string
	if isSimplified {
		// For simplified invoices, generate QR with all 9 tags
		qrData := &QRData{
			Hash:      invoiceHash,
			Signature: signatureBytes,
			PublicKey: cert.PublicKeyBytes(),
			CertHash:  cert.Raw, // DER-encoded certificate
		}
		// Extract seller/VAT/total from XML (caller should set these)
		qrCode, _ = GenerateQR(qrData)
	}

	// Step 9: If QR generated, inject into XML
	if qrCode != "" {
		signedXML = injectQR(signedXML, qrCode)
	}

	return &SignResult{
		SignedXML:    signedXML,
		InvoiceHash: invoiceHash,
		SignatureB64: signatureB64,
		QRCode:       qrCode,
	}, nil
}

// SignInvoiceWithQR signs and generates QR with full invoice data.
func SignInvoiceWithQR(unsignedXML string, privateKey *ecdsa.PrivateKey, cert *CertInfo,
	isSimplified bool, sellerName, vatNumber string, timestamp time.Time, total, vatAmount string) (*SignResult, error) {

	// Step 1: Compute invoice hash
	invoiceHash, err := InvoiceHash(unsignedXML)
	if err != nil {
		return nil, fmt.Errorf("computing invoice hash: %w", err)
	}

	// Step 2: Build XAdES
	signingTime := time.Now().Format("2006-01-02T15:04:05")
	signedProps := buildSignedProperties(signingTime, cert)
	signedPropsHash := sha256.Sum256([]byte(signedProps))
	signedPropsDigestHex := fmt.Sprintf("%x", signedPropsHash[:])
	signedPropsDigest := base64.StdEncoding.EncodeToString([]byte(signedPropsDigestHex))

	signedInfo := buildSignedInfo(invoiceHash, signedPropsDigest)
	signedInfoHash := sha256.Sum256([]byte(signedInfo))

	r, s, err := ecdsa.Sign(rand.Reader, privateKey, signedInfoHash[:])
	if err != nil {
		return nil, fmt.Errorf("ECDSA sign: %w", err)
	}

	signatureBytes := encodeECDSASignatureDER(r, s)
	signatureB64 := base64.StdEncoding.EncodeToString(signatureBytes)

	dsSignature := buildDSSignature(signedInfo, signatureB64, cert, signedProps)
	signedXML := injectSignature(unsignedXML, dsSignature)

	// Generate QR
	var qrCode string
	qrData := &QRData{
		SellerName: sellerName,
		VATNumber:  vatNumber,
		Timestamp:  timestamp,
		Total:      total,
		VATAmount:  vatAmount,
		Hash:       invoiceHash,
		Signature:  signatureBytes,
		PublicKey:  cert.PublicKeyBytes(),
		CertHash:   cert.Raw,
	}

	if isSimplified {
		// Simplified invoices get full Phase 2 QR (all 9 tags)
		qrCode, err = GenerateQR(qrData)
	} else {
		// Standard invoices still get QR but may be optional
		qrCode, err = GenerateQR(qrData)
	}
	if err != nil {
		return nil, fmt.Errorf("generating QR: %w", err)
	}

	signedXML = injectQR(signedXML, qrCode)

	return &SignResult{
		SignedXML:    signedXML,
		InvoiceHash: invoiceHash,
		SignatureB64: signatureB64,
		QRCode:       qrCode,
	}, nil
}

// buildSignedProperties builds the xades:SignedProperties XML block.
func buildSignedProperties(signingTime string, cert *CertInfo) string {
	var b strings.Builder
	b.WriteString(`<xades:SignedProperties xmlns:xades="http://uri.etsi.org/01903/v1.3.2#" Id="xadesSignedProperties">`)
	b.WriteString(`<xades:SignedSignatureProperties>`)
	b.WriteString(`<xades:SigningTime>`)
	b.WriteString(signingTime)
	b.WriteString(`</xades:SigningTime>`)
	b.WriteString(`<xades:SigningCertificate>`)
	b.WriteString(`<xades:Cert>`)
	b.WriteString(`<xades:CertDigest>`)
	b.WriteString(`<ds:DigestMethod xmlns:ds="http://www.w3.org/2000/09/xmldsig#" Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/>`)
	b.WriteString(`<ds:DigestValue xmlns:ds="http://www.w3.org/2000/09/xmldsig#">`)
	b.WriteString(cert.CertDigest())
	b.WriteString(`</ds:DigestValue>`)
	b.WriteString(`</xades:CertDigest>`)
	b.WriteString(`<xades:IssuerSerial>`)
	b.WriteString(`<ds:X509IssuerName xmlns:ds="http://www.w3.org/2000/09/xmldsig#">`)
	b.WriteString(cert.IssuerNameForXML())
	b.WriteString(`</ds:X509IssuerName>`)
	b.WriteString(`<ds:X509SerialNumber xmlns:ds="http://www.w3.org/2000/09/xmldsig#">`)
	b.WriteString(cert.SerialNumberDecimal())
	b.WriteString(`</ds:X509SerialNumber>`)
	b.WriteString(`</xades:IssuerSerial>`)
	b.WriteString(`</xades:Cert>`)
	b.WriteString(`</xades:SigningCertificate>`)
	b.WriteString(`</xades:SignedSignatureProperties>`)
	b.WriteString(`</xades:SignedProperties>`)
	return b.String()
}

// buildSignedInfo builds the ds:SignedInfo XML block.
func buildSignedInfo(invoiceDigest, signedPropsDigest string) string {
	var b strings.Builder
	b.WriteString(`<ds:SignedInfo xmlns:ds="http://www.w3.org/2000/09/xmldsig#">`)
	b.WriteString(`<ds:CanonicalizationMethod Algorithm="http://www.w3.org/2006/12/xml-c14n11"/>`)
	b.WriteString(`<ds:SignatureMethod Algorithm="http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha256"/>`)

	// Reference 1: Invoice signed data
	b.WriteString(`<ds:Reference Id="invoiceSignedData" URI="">`)
	b.WriteString(`<ds:Transforms>`)
	b.WriteString(`<ds:Transform Algorithm="http://www.w3.org/TR/1999/REC-xpath-19991116">`)
	b.WriteString(`<ds:XPath>not(//ancestor-or-self::ext:UBLExtensions)</ds:XPath>`)
	b.WriteString(`</ds:Transform>`)
	b.WriteString(`<ds:Transform Algorithm="http://www.w3.org/TR/1999/REC-xpath-19991116">`)
	b.WriteString(`<ds:XPath>not(//ancestor-or-self::cac:Signature)</ds:XPath>`)
	b.WriteString(`</ds:Transform>`)
	b.WriteString(`<ds:Transform Algorithm="http://www.w3.org/TR/1999/REC-xpath-19991116">`)
	b.WriteString(`<ds:XPath>not(//ancestor-or-self::cac:AdditionalDocumentReference[cbc:ID='QR'])</ds:XPath>`)
	b.WriteString(`</ds:Transform>`)
	b.WriteString(`<ds:Transform Algorithm="http://www.w3.org/2006/12/xml-c14n11"/>`)
	b.WriteString(`</ds:Transforms>`)
	b.WriteString(`<ds:DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/>`)
	b.WriteString(`<ds:DigestValue>`)
	b.WriteString(invoiceDigest)
	b.WriteString(`</ds:DigestValue>`)
	b.WriteString(`</ds:Reference>`)

	// Reference 2: XAdES SignedProperties
	b.WriteString(`<ds:Reference Type="http://www.w3.org/2000/09/xmldsig#SignatureProperties" URI="#xadesSignedProperties">`)
	b.WriteString(`<ds:DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/>`)
	b.WriteString(`<ds:DigestValue>`)
	b.WriteString(signedPropsDigest)
	b.WriteString(`</ds:DigestValue>`)
	b.WriteString(`</ds:Reference>`)

	b.WriteString(`</ds:SignedInfo>`)
	return b.String()
}

// buildDSSignature builds the complete ds:Signature XML block.
func buildDSSignature(signedInfo, signatureValue string, cert *CertInfo, signedProps string) string {
	var b strings.Builder
	b.WriteString(`                    <ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#" Id="signature">` + "\n")
	b.WriteString("                        ")
	b.WriteString(signedInfo)
	b.WriteString("\n")
	b.WriteString("                        <ds:SignatureValue>")
	b.WriteString(signatureValue)
	b.WriteString("</ds:SignatureValue>\n")
	b.WriteString("                        <ds:KeyInfo>\n")
	b.WriteString("                            <ds:X509Data>\n")
	b.WriteString("                                <ds:X509Certificate>")
	b.WriteString(cert.CertBase64())
	b.WriteString("</ds:X509Certificate>\n")
	b.WriteString("                            </ds:X509Data>\n")
	b.WriteString("                        </ds:KeyInfo>\n")
	b.WriteString("                        <ds:Object>\n")
	b.WriteString(`                            <xades:QualifyingProperties xmlns:xades="http://uri.etsi.org/01903/v1.3.2#" Target="signature">` + "\n")
	b.WriteString("                                ")
	b.WriteString(signedProps)
	b.WriteString("\n")
	b.WriteString("                            </xades:QualifyingProperties>\n")
	b.WriteString("                        </ds:Object>\n")
	b.WriteString("                    </ds:Signature>")
	return b.String()
}

// injectSignature replaces the signature placeholder in UBLExtensions
// with the actual ds:Signature XML.
func injectSignature(xml, dsSignature string) string {
	// Find the SignatureInformation block and inject the signature after ReferencedSignatureID
	marker := `</sbc:ReferencedSignatureID>`
	closingMarker := `</sac:SignatureInformation>`

	markerIdx := strings.Index(xml, marker)
	if markerIdx < 0 {
		return xml
	}
	insertPoint := markerIdx + len(marker)

	closingIdx := strings.Index(xml[insertPoint:], closingMarker)
	if closingIdx < 0 {
		return xml
	}

	// Replace whatever was between the markers with the new signature
	return xml[:insertPoint] + "\n" + dsSignature + "\n                " + xml[insertPoint+closingIdx:]
}

// injectQR replaces the QR code placeholder in AdditionalDocumentReference.
func injectQR(xml, qrBase64 string) string {
	// Find the QR EmbeddedDocumentBinaryObject and set its content
	qrStart := `<cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain"></cbc:EmbeddedDocumentBinaryObject>`
	qrReplace := fmt.Sprintf(`<cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain">%s</cbc:EmbeddedDocumentBinaryObject>`, qrBase64)

	// Only replace in QR context (after the QR ID)
	qrIDIdx := strings.LastIndex(xml, `<cbc:ID>QR</cbc:ID>`)
	if qrIDIdx < 0 {
		return xml
	}

	before := xml[:qrIDIdx]
	after := xml[qrIDIdx:]

	// Replace only in the 'after' part
	after = strings.Replace(after, qrStart, qrReplace, 1)
	return before + after
}

// encodeECDSASignatureDER encodes an ECDSA signature (r, s) in DER format.
func encodeECDSASignatureDER(r, s *big.Int) []byte {
	rBytes := intToASN1(r)
	sBytes := intToASN1(s)

	// SEQUENCE { INTEGER r, INTEGER s }
	inner := make([]byte, 0, len(rBytes)+len(sBytes))
	inner = append(inner, rBytes...)
	inner = append(inner, sBytes...)

	result := make([]byte, 0, 2+len(inner))
	result = append(result, 0x30)                 // SEQUENCE tag
	result = append(result, byte(len(inner)))      // length
	result = append(result, inner...)
	return result
}

// intToASN1 encodes a big.Int as an ASN.1 INTEGER.
func intToASN1(n *big.Int) []byte {
	b := n.Bytes()
	// Pad with 0x00 if high bit is set (positive integer)
	if len(b) > 0 && b[0]&0x80 != 0 {
		b = append([]byte{0x00}, b...)
	}
	result := make([]byte, 0, 2+len(b))
	result = append(result, 0x02)          // INTEGER tag
	result = append(result, byte(len(b)))  // length
	result = append(result, b...)
	return result
}
