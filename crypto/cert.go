package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"

	dcrsecp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// CertInfo holds parsed certificate information needed for ZATCA signing.
type CertInfo struct {
	Certificate *x509.Certificate
	Raw         []byte // DER-encoded certificate bytes
	PublicKey   *ecdsa.PublicKey
	IssuerName  string
	SerialNum   *big.Int
}

// ParseCertificate parses a base64-encoded X.509 certificate.
// ZATCA returns BinarySecurityToken as base64(base64(DER)), so this handles
// both single and double base64 encoding.
func ParseCertificate(base64Cert string) (*CertInfo, error) {
	derBytes, err := base64.StdEncoding.DecodeString(base64Cert)
	if err != nil {
		return nil, fmt.Errorf("base64 decode certificate: %w", err)
	}

	// Try parsing as DER directly first
	info, err := parseCertDER(derBytes)
	if err == nil {
		return info, nil
	}

	// If that fails, the decoded bytes might be another base64 string (double-encoded)
	innerDER, err2 := base64.StdEncoding.DecodeString(string(derBytes))
	if err2 != nil {
		return nil, fmt.Errorf("parse X.509 certificate: %w", err)
	}
	return parseCertDER(innerDER)
}

// ParseCertificatePEM parses a PEM-encoded X.509 certificate.
func ParseCertificatePEM(pemData []byte) (*CertInfo, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}
	return parseCertDER(block.Bytes)
}

func parseCertDER(derBytes []byte) (*CertInfo, error) {
	// Try standard parsing first (works for NIST curves)
	cert, err := x509.ParseCertificate(derBytes)
	if err == nil {
		pubKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("certificate public key is not ECDSA")
		}
		return &CertInfo{
			Certificate: cert,
			Raw:         derBytes,
			PublicKey:   pubKey,
			IssuerName:  cert.Issuer.String(),
			SerialNum:   cert.SerialNumber,
		}, nil
	}

	// Standard parsing failed (likely secp256k1 unsupported curve).
	// Fall back to manual ASN.1 parsing for the fields we need.
	return parseCertDERManual(derBytes)
}

// secp256k1 OID: 1.3.132.0.10
var oidSecp256k1 = asn1.ObjectIdentifier{1, 3, 132, 0, 10}

// ASN.1 structures for manual X.509 certificate parsing
type tbsCertificate struct {
	Raw          asn1.RawContent
	Version      asn1.RawValue `asn1:"optional,explicit,default:0,tag:0"`
	SerialNumber *big.Int
	Signature    asn1.RawValue
	Issuer       asn1.RawValue
	Validity     asn1.RawValue
	Subject      asn1.RawValue
	PublicKey    subjectPublicKeyInfo
}

type subjectPublicKeyInfo struct {
	Algorithm algorithmIdentifier
	PublicKey asn1.BitString
}

type algorithmIdentifier struct {
	Algorithm asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type certificate struct {
	TBSCertificate tbsCertificate
	SignatureAlgo  asn1.RawValue
	Signature      asn1.BitString
}

func parseCertDERManual(derBytes []byte) (*CertInfo, error) {
	var cert certificate
	rest, err := asn1.Unmarshal(derBytes, &cert)
	if err != nil {
		return nil, fmt.Errorf("asn1 parse certificate: %w", err)
	}
	if len(rest) > 0 {
		return nil, fmt.Errorf("trailing data after certificate")
	}

	tbs := cert.TBSCertificate

	// Parse issuer name from raw DER
	issuerName, err := parseIssuerDN(tbs.Issuer.FullBytes)
	if err != nil {
		issuerName = "CN=unknown"
	}

	// Extract public key
	pubKeyBytes := tbs.PublicKey.PublicKey.Bytes
	pubKey, err := parseSecp256k1PublicKey(pubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse secp256k1 public key: %w", err)
	}

	return &CertInfo{
		Raw:        derBytes,
		PublicKey:  pubKey,
		IssuerName: issuerName,
		SerialNum:  tbs.SerialNumber,
	}, nil
}

func parseSecp256k1PublicKey(data []byte) (*ecdsa.PublicKey, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty public key")
	}
	// Uncompressed point: 0x04 || X (32 bytes) || Y (32 bytes)
	if data[0] == 0x04 && len(data) == 65 {
		x := new(big.Int).SetBytes(data[1:33])
		y := new(big.Int).SetBytes(data[33:65])
		return &ecdsa.PublicKey{
			Curve: secp256k1Curve(),
			X:     x,
			Y:     y,
		}, nil
	}
	return nil, fmt.Errorf("unsupported public key format (len=%d, prefix=0x%02x)", len(data), data[0])
}

// secp256k1Curve returns the secp256k1 elliptic curve.
func secp256k1Curve() elliptic.Curve {
	return dcrsecp256k1.S256()
}

func parseIssuerDN(raw []byte) (string, error) {
	// Parse as RDNSequence (same as Go's pkix)
	var rdnSeq [][]pkixAttributeTypeAndValue
	_, err := asn1.Unmarshal(raw, &rdnSeq)
	if err != nil {
		return "", err
	}

	parts := make([]string, 0, len(rdnSeq))
	for _, rdnSet := range rdnSeq {
		for _, atv := range rdnSet {
			name := oidToName(atv.Type)
			parts = append(parts, fmt.Sprintf("%s=%s", name, atvValueString(atv.Value)))
		}
	}
	return joinReverse(parts), nil
}

type pkixAttributeTypeAndValue struct {
	Type  asn1.ObjectIdentifier
	Value asn1.RawValue
}

func atvValueString(raw asn1.RawValue) string {
	// Try to decode as string regardless of ASN.1 tag type
	// (UTF8String, PrintableString, IA5String, etc.)
	return string(raw.Bytes)
}

func oidToName(oid asn1.ObjectIdentifier) string {
	switch {
	case oid.Equal(asn1.ObjectIdentifier{2, 5, 4, 3}):
		return "CN"
	case oid.Equal(asn1.ObjectIdentifier{2, 5, 4, 6}):
		return "C"
	case oid.Equal(asn1.ObjectIdentifier{2, 5, 4, 10}):
		return "O"
	case oid.Equal(asn1.ObjectIdentifier{2, 5, 4, 11}):
		return "OU"
	default:
		return oid.String()
	}
}

func joinReverse(parts []string) string {
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += ","
		}
		result += p
	}
	return result
}

// CertDigest returns the SHA-256 hash of the certificate (DER encoded),
// formatted as hex string then base64 encoded (ZATCA format).
func (ci *CertInfo) CertDigest() string {
	hash := sha256.Sum256(ci.Raw)
	hexStr := fmt.Sprintf("%x", hash[:])
	return base64.StdEncoding.EncodeToString([]byte(hexStr))
}

// CertBase64 returns the raw certificate as base64 (no PEM headers, no newlines).
func (ci *CertInfo) CertBase64() string {
	return base64.StdEncoding.EncodeToString(ci.Raw)
}

// PublicKeyBytes returns the uncompressed public key bytes (65 bytes: 04 || X || Y).
func (ci *CertInfo) PublicKeyBytes() []byte {
	// For secp256k1, public key is 32 bytes X + 32 bytes Y
	x := ci.PublicKey.X.Bytes()
	y := ci.PublicKey.Y.Bytes()

	// Pad to 32 bytes each
	pubKey := make([]byte, 65)
	pubKey[0] = 0x04 // uncompressed point
	copy(pubKey[1+32-len(x):33], x)
	copy(pubKey[33+32-len(y):65], y)
	return pubKey
}

// IssuerNameForXML returns the issuer name for XML signature.
func (ci *CertInfo) IssuerNameForXML() string {
	if ci.Certificate != nil {
		return ci.Certificate.Issuer.String()
	}
	return ci.IssuerName
}

// SerialNumberDecimal returns the certificate serial number as decimal string.
func (ci *CertInfo) SerialNumberDecimal() string {
	return ci.SerialNum.String()
}

// ParsePrivateKey parses a PEM-encoded EC private key.
func ParsePrivateKey(pemData []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM private key")
	}

	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err == nil {
		return key, nil
	}

	// Try PKCS8 format
	pkcs8Key, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err2 == nil {
		ecKey, ok := pkcs8Key.(*ecdsa.PrivateKey)
		if ok {
			return ecKey, nil
		}
	}

	// Standard parsing failed — likely secp256k1. Parse PKCS8 manually.
	return parseSecp256k1PrivateKey(block.Bytes)
}

// PKCS8 ASN.1 structure for manual parsing
type pkcs8PrivateKey struct {
	Version    int
	Algorithm  algorithmIdentifier
	PrivateKey []byte
}

// EC private key ASN.1 structure (RFC 5915)
type ecPrivateKey struct {
	Version       int
	PrivateKey    []byte
	NamedCurveOID asn1.ObjectIdentifier `asn1:"optional,explicit,tag:0"`
	PublicKey     asn1.BitString        `asn1:"optional,explicit,tag:1"`
}

func parseSecp256k1PrivateKey(der []byte) (*ecdsa.PrivateKey, error) {
	// Try PKCS8 structure first
	var pkcs8 pkcs8PrivateKey
	if _, err := asn1.Unmarshal(der, &pkcs8); err == nil {
		// Extract curve OID from algorithm parameters
		var curveOID asn1.ObjectIdentifier
		if _, err := asn1.Unmarshal(pkcs8.Algorithm.Parameters.FullBytes, &curveOID); err == nil {
			if curveOID.Equal(oidSecp256k1) {
				// Parse the inner EC private key
				var ecKey ecPrivateKey
				if _, err := asn1.Unmarshal(pkcs8.PrivateKey, &ecKey); err != nil {
					return nil, fmt.Errorf("parse inner EC key: %w", err)
				}
				return buildSecp256k1Key(ecKey.PrivateKey, ecKey.PublicKey.Bytes)
			}
		}
	}

	// Try bare EC private key structure
	var ecKey ecPrivateKey
	if _, err := asn1.Unmarshal(der, &ecKey); err == nil {
		if ecKey.NamedCurveOID.Equal(oidSecp256k1) || len(ecKey.NamedCurveOID) == 0 {
			return buildSecp256k1Key(ecKey.PrivateKey, ecKey.PublicKey.Bytes)
		}
	}

	return nil, fmt.Errorf("failed to parse secp256k1 private key")
}

func buildSecp256k1Key(privBytes []byte, pubBytes []byte) (*ecdsa.PrivateKey, error) {
	curve := secp256k1Curve()
	d := new(big.Int).SetBytes(privBytes)

	var x, y *big.Int
	if len(pubBytes) == 65 && pubBytes[0] == 0x04 {
		// Uncompressed public key provided
		x = new(big.Int).SetBytes(pubBytes[1:33])
		y = new(big.Int).SetBytes(pubBytes[33:65])
	} else {
		return nil, fmt.Errorf("secp256k1 key missing embedded public key")
	}

	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: curve,
			X:     x,
			Y:     y,
		},
		D: d,
	}, nil
}
