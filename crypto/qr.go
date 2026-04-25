package crypto

import (
	"encoding/base64"
	"fmt"
	"time"
)

// QR TLV tag numbers per ZATCA Phase 2 specification.
const (
	TagSellerName   = 1
	TagVATNumber    = 2
	TagTimestamp    = 3
	TagTotalWithVAT = 4
	TagVATAmount    = 5
	TagInvoiceHash  = 6
	TagSignature    = 7
	TagPublicKey    = 8
	TagCertHash     = 9
)

// QRData holds the data needed for ZATCA QR code generation.
type QRData struct {
	SellerName string    // Tag 1: Seller name (UTF-8)
	VATNumber  string    // Tag 2: VAT registration number (15 digits)
	Timestamp  time.Time // Tag 3: Invoice timestamp
	Total      string    // Tag 4: Total with VAT (decimal string, e.g., "115.00")
	VATAmount  string    // Tag 5: VAT amount (decimal string, e.g., "15.00")
	Hash       string    // Tag 6: Invoice hash (base64)
	Signature  []byte    // Tag 7: ECDSA digital signature (raw bytes)
	PublicKey  []byte    // Tag 8: ECDSA public key (uncompressed, 33 or 65 bytes)
	CertHash   []byte    // Tag 9: Certificate signature / hash (DER encoded)
}

// GenerateQR generates a ZATCA Phase 2 QR code as base64-encoded TLV data.
func GenerateQR(data *QRData) (string, error) {
	if data.SellerName == "" {
		return "", fmt.Errorf("seller name is required for QR")
	}
	if data.VATNumber == "" {
		return "", fmt.Errorf("VAT number is required for QR")
	}

	var tlv []byte

	// Tag 1: Seller name
	tlv = appendTLV(tlv, TagSellerName, []byte(data.SellerName))

	// Tag 2: VAT number
	tlv = appendTLV(tlv, TagVATNumber, []byte(data.VATNumber))

	// Tag 3: Timestamp (ISO 8601)
	ts := data.Timestamp.Format("2006-01-02T15:04:05Z")
	tlv = appendTLV(tlv, TagTimestamp, []byte(ts))

	// Tag 4: Total with VAT
	tlv = appendTLV(tlv, TagTotalWithVAT, []byte(data.Total))

	// Tag 5: VAT amount
	tlv = appendTLV(tlv, TagVATAmount, []byte(data.VATAmount))

	// Tag 6: Invoice hash (base64 string)
	if data.Hash != "" {
		tlv = appendTLV(tlv, TagInvoiceHash, []byte(data.Hash))
	}

	// Tag 7: Digital signature (raw bytes)
	if len(data.Signature) > 0 {
		tlv = appendTLV(tlv, TagSignature, data.Signature)
	}

	// Tag 8: Public key (raw bytes)
	if len(data.PublicKey) > 0 {
		tlv = appendTLV(tlv, TagPublicKey, data.PublicKey)
	}

	// Tag 9: Certificate signature/hash
	if len(data.CertHash) > 0 {
		tlv = appendTLV(tlv, TagCertHash, data.CertHash)
	}

	return base64.StdEncoding.EncodeToString(tlv), nil
}

// GenerateSimpleQR generates a ZATCA Phase 1 QR code (tags 1-5 only).
func GenerateSimpleQR(sellerName, vatNumber string, timestamp time.Time, total, vatAmount string) (string, error) {
	return GenerateQR(&QRData{
		SellerName: sellerName,
		VATNumber:  vatNumber,
		Timestamp:  timestamp,
		Total:      total,
		VATAmount:  vatAmount,
	})
}

// appendTLV appends a TLV (Tag-Length-Value) entry to the byte slice.
// Tag: 1 byte, Length: 1 or 2 bytes (if >127, use 2-byte big-endian), Value: raw bytes.
func appendTLV(buf []byte, tag int, value []byte) []byte {
	buf = append(buf, byte(tag))

	length := len(value)
	if length <= 127 {
		buf = append(buf, byte(length))
	} else {
		// Multi-byte length: first byte = 0x82, then 2-byte big-endian length
		buf = append(buf, 0x82, byte(length>>8), byte(length&0xFF))
	}

	buf = append(buf, value...)
	return buf
}

// DecodeTLV decodes TLV-encoded QR data for debugging.
func DecodeTLV(b64 string) (map[int][]byte, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	result := make(map[int][]byte)
	i := 0
	for i < len(data) {
		if i+2 > len(data) {
			return nil, fmt.Errorf("truncated TLV at offset %d", i)
		}
		tag := int(data[i])
		i++

		var length int
		if data[i] == 0x82 {
			// Multi-byte length
			if i+3 > len(data) {
				return nil, fmt.Errorf("truncated multi-byte length at offset %d", i)
			}
			length = int(data[i+1])<<8 | int(data[i+2])
			i += 3
		} else {
			length = int(data[i])
			i++
		}

		if i+length > len(data) {
			return nil, fmt.Errorf("TLV value overflow at tag %d, offset %d", tag, i)
		}
		value := make([]byte, length)
		copy(value, data[i:i+length])
		result[tag] = value
		i += length
	}

	return result, nil
}
