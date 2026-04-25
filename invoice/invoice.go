package invoice

import (
	"fmt"
	"time"
)

// TypeCode represents the UBL invoice type.
type TypeCode int

const (
	TypeInvoice    TypeCode = 388
	TypeCreditNote TypeCode = 381
	TypeDebitNote  TypeCode = 383
)

// SubType represents the invoice sub-type (standard vs simplified).
type SubType string

const (
	SubTypeStandard   SubType = "0100000" // B2B
	SubTypeSimplified SubType = "0200000" // B2C
)

// TaxCategory represents VAT category codes per UN/ECE 5305.
type TaxCategory string

const (
	TaxStandard TaxCategory = "S"  // Standard rated (15% or 5%)
	TaxZero     TaxCategory = "Z"  // Zero rated
	TaxExempt   TaxCategory = "E"  // Exempt
	TaxOutScope TaxCategory = "O"  // Out of scope
)

// PaymentMeans codes.
const (
	PaymentCash     = "10"
	PaymentCredit   = "30"
	PaymentBankCard = "48"
	PaymentTransfer = "42"
)

// Party holds information about a business party.
type Party struct {
	RegistrationName   string
	RegistrationNameAR string
	VAT                string  // 15-digit VAT number
	SchemeID           string  // CRN, MOM, MLS, SAG, OTH, etc.
	ID                 string  // registration number value
	Street             string
	Building           string
	District           string
	City               string
	PostalCode         string
	Country            string  // ISO 3166-1 alpha-2 (SA)
}

// LineItem represents a single invoice line.
type LineItem struct {
	ID          string
	Name        string
	Quantity    float64
	UnitCode    string  // PCE, EA, etc.
	UnitPrice   float64
	Discount    float64 // line-level discount
	TaxCategory TaxCategory
	TaxPercent  float64 // e.g., 15.00
}

// NetAmount returns quantity * unitPrice - discount.
func (l *LineItem) NetAmount() float64 {
	return roundTo2(l.Quantity*l.UnitPrice - l.Discount)
}

// TaxAmount returns the tax on the net amount.
func (l *LineItem) TaxAmount() float64 {
	return roundTo2(l.NetAmount() * l.TaxPercent / 100)
}

// TotalWithTax returns net + tax.
func (l *LineItem) TotalWithTax() float64 {
	return roundTo2(l.NetAmount() + l.TaxAmount())
}

// Invoice holds all data needed to generate a UBL 2.1 XML invoice.
type Invoice struct {
	// Metadata
	ID        string    // Invoice number (e.g., SME00023)
	UUID      string    // Unique identifier (UUID v4)
	IssueDate time.Time
	IssueTime time.Time // only time part used

	// Type
	TypeCode TypeCode
	SubType  SubType

	// Currency
	DocumentCurrency string // SAR
	TaxCurrency      string // SAR

	// Note (optional)
	Note     string
	NoteLang string // e.g., "ar"

	// Parties
	Supplier Party
	Customer Party

	// Delivery
	DeliveryDate time.Time

	// Payment
	PaymentMeans    string
	InstructionNote string // KSA-10: reason for credit/debit note

	// Billing reference (for credit/debit notes)
	BillingReferenceID string

	// Document level discount
	Discount     float64
	DiscountTax  TaxCategory
	DiscountTaxPercent float64

	// Line items
	Lines []LineItem

	// Hash chain
	PreviousInvoiceHash string // base64 hash of previous invoice
	InvoiceCounterValue int    // ICV - sequential counter

	// These are populated during signing
	ProfileID string // "reporting:1.0" or "clearance:1.0" - set automatically
}

// TaxableAmount returns the sum of all line net amounts.
func (inv *Invoice) TaxableAmount() float64 {
	total := -inv.Discount
	for _, l := range inv.Lines {
		total += l.NetAmount()
	}
	return roundTo2(total)
}

// TotalTax returns the total VAT amount.
func (inv *Invoice) TotalTax() float64 {
	total := 0.0
	for _, l := range inv.Lines {
		total += l.TaxAmount()
	}
	// Subtract discount tax if applicable
	if inv.Discount > 0 && inv.DiscountTaxPercent > 0 {
		total -= roundTo2(inv.Discount * inv.DiscountTaxPercent / 100)
	}
	return roundTo2(total)
}

// TotalWithTax returns taxable + tax.
func (inv *Invoice) TotalWithTax() float64 {
	return roundTo2(inv.TaxableAmount() + inv.TotalTax())
}

// IsSimplified returns true if this is a B2C invoice.
func (inv *Invoice) IsSimplified() bool {
	return inv.SubType == SubTypeSimplified
}

// IsStandard returns true if this is a B2B invoice.
func (inv *Invoice) IsStandard() bool {
	return inv.SubType == SubTypeStandard
}

func (inv *Invoice) profileID() string {
	if inv.ProfileID != "" {
		return inv.ProfileID
	}
	if inv.IsStandard() {
		return "reporting:1.0"
	}
	return "reporting:1.0"
}

func (inv *Invoice) dateStr() string {
	return inv.IssueDate.Format("2006-01-02")
}

func (inv *Invoice) timeStr() string {
	return inv.IssueTime.Format("15:04:05")
}

func (inv *Invoice) deliveryDateStr() string {
	if inv.DeliveryDate.IsZero() {
		return inv.dateStr()
	}
	return inv.DeliveryDate.Format("2006-01-02")
}

func roundTo2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

func fmtAmount(v float64) string {
	return fmt.Sprintf("%.2f", v)
}
