package crypto

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// Namespace URIs used for element matching during stripping.
const (
	nsExt = "urn:oasis:names:specification:ubl:schema:xsd:CommonExtensionComponents-2"
	nsCAC = "urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
	nsCBC = "urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2"
)

// InvoiceHash computes the SHA-256 hash of an invoice XML for ZATCA.
// It removes UBLExtensions, cac:Signature, and QR AdditionalDocumentReference
// at the DOM level, then applies XML C14N11 canonicalization before hashing.
// Returns base64-encoded hash.
func InvoiceHash(xmlContent string) (string, error) {
	canonicalized, err := canonicalizeForHashing(xmlContent)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(canonicalized)
	return base64.StdEncoding.EncodeToString(hash[:]), nil
}

// InvoiceHashBytes returns the raw SHA-256 hash bytes (32 bytes).
func InvoiceHashBytes(xmlContent string) ([]byte, error) {
	canonicalized, err := canonicalizeForHashing(xmlContent)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(canonicalized)
	return hash[:], nil
}

// canonicalizeForHashing removes 3 ZATCA-excluded sections at DOM level and applies C14N11.
func canonicalizeForHashing(xmlContent string) ([]byte, error) {
	doc := etree.NewDocument()
	doc.WriteSettings.CanonicalEndTags = true
	doc.WriteSettings.CanonicalText = true
	doc.WriteSettings.CanonicalAttrVal = true
	if err := doc.ReadFromString(xmlContent); err != nil {
		return nil, fmt.Errorf("parsing XML: %w", err)
	}

	root := doc.Root()
	if root == nil {
		return nil, fmt.Errorf("no root element")
	}

	// Remove ext:UBLExtensions
	removeChildElement(root, nsExt, "UBLExtensions")

	// Remove cac:Signature (invoice-level signature reference, NOT ds:Signature)
	removeChildElement(root, nsCAC, "Signature")

	// Remove cac:AdditionalDocumentReference where cbc:ID = 'QR'
	removeQRReference(root)

	// Apply C14N11 canonicalization
	c14n := dsig.MakeC14N11Canonicalizer()
	canonicalized, err := c14n.Canonicalize(root)
	if err != nil {
		return nil, fmt.Errorf("C14N11: %w", err)
	}

	return canonicalized, nil
}

// removeChildElement removes the first direct child element matching the given namespace+local name.
func removeChildElement(parent *etree.Element, space, tag string) {
	for _, child := range parent.ChildElements() {
		if child.Space != "" {
			// Compare by resolved namespace URI
			ns := resolveNamespace(parent, child.Space)
			if ns == space && child.Tag == tag {
				parent.RemoveChild(child)
				return
			}
		}
		if child.Tag == tag {
			// Try matching by prefix mapping
			for _, attr := range parent.Attr {
				if attr.Key == child.Space && attr.Space == "xmlns" && attr.Value == space {
					parent.RemoveChild(child)
					return
				}
			}
		}
	}
}

// resolveNamespace resolves a namespace prefix to its URI by walking up the tree.
func resolveNamespace(el *etree.Element, prefix string) string {
	for e := el; e != nil; e = e.Parent() {
		for _, attr := range e.Attr {
			if attr.Space == "xmlns" && attr.Key == prefix {
				return attr.Value
			}
			if attr.Space == "" && attr.Key == "xmlns" && prefix == "" {
				return attr.Value
			}
		}
	}
	return ""
}

// removeQRReference removes the cac:AdditionalDocumentReference containing QR.
func removeQRReference(root *etree.Element) {
	for _, child := range root.ChildElements() {
		if child.Tag == "AdditionalDocumentReference" {
			// Check if this reference has cbc:ID = "QR"
			for _, sub := range child.ChildElements() {
				if sub.Tag == "ID" && strings.TrimSpace(sub.Text()) == "QR" {
					root.RemoveChild(child)
					return
				}
			}
		}
	}
}

// ExportStripForHashing exports the stripped (but not canonicalized) XML for debugging.
func ExportStripForHashing(xmlContent string) string {
	doc := etree.NewDocument()
	if err := doc.ReadFromString(xmlContent); err != nil {
		return xmlContent
	}
	root := doc.Root()
	if root == nil {
		return xmlContent
	}

	removeChildElement(root, nsExt, "UBLExtensions")
	removeChildElement(root, nsCAC, "Signature")
	removeQRReference(root)

	doc.Indent(0)
	result, _ := doc.WriteToString()
	return result
}

// ExportCanonicalizedXML returns the C14N11 output for debugging.
func ExportCanonicalizedXML(xmlContent string) (string, error) {
	b, err := canonicalizeForHashing(xmlContent)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// PIHHash computes the previous invoice hash for the hash chain.
// Takes the full signed invoice XML and returns its base64-encoded SHA-256 hash.
func PIHHash(signedXML string) string {
	hash := sha256.Sum256([]byte(signedXML))
	return base64.StdEncoding.EncodeToString(hash[:])
}
