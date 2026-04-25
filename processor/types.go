package processor

import (
	"database/sql"
	"fmt"
	"time"
)

const DefaultPIH = "NWZlY2ViNjZmZmM4NmYzOGQ5NTI3ODZjNmQ2OTZjNzljMmRiYzIzOWRkNGU5MWI0NjcyOWQ3M2EyN2ZiNTdlOQ=="

// ProductRow represents product columns from the bill_product table.
type ProductRow struct {
	ProductID         sql.NullInt64
	Name              sql.NullString
	Price             sql.NullFloat64
	Quantity          sql.NullFloat64
	VAT               sql.NullFloat64
	Discount          sql.NullFloat64
	TotalBeforeVAT    sql.NullFloat64
	VATTotal          sql.NullFloat64
	TotalIncludingVAT sql.NullFloat64
}

func (p *ProductRow) PriceOrZero() float64 {
	if p.Price.Valid {
		return p.Price.Float64
	}
	return 0
}

func (p *ProductRow) QuantityOrZero() float64 {
	if p.Quantity.Valid {
		return p.Quantity.Float64
	}
	return 1
}

func (p *ProductRow) VATOrZero() float64 {
	if p.VAT.Valid {
		return p.VAT.Float64
	}
	return 15.0
}

// BillRow holds scanned bill row data used for building invoices.
type BillRow struct {
	ID                int64
	BranchID          int64
	CompanyID         int64
	URL               sql.NullString
	EffectiveDate     sql.NullString
	PaymentDue        sql.NullString
	SeqNumber         sql.NullString
	PaymentMethod     sql.NullString
	UserName          sql.NullString
	CompanyName       sql.NullString
	VATReg            sql.NullString
	AddressName       sql.NullString
	BuyerName         sql.NullString
	BuyerCompany      sql.NullString
	BuyerVAT          sql.NullString
	BuyerAddress      sql.NullString
	BuyerStreet       sql.NullString
	BuyerBuilding     sql.NullString
	BuyerDistrict     sql.NullString
	BuyerCity         sql.NullString
	BuyerPostalCode   sql.NullString
	BuyerCountry      sql.NullString
	BuyerSchemeID     sql.NullString
	BuyerRegistration sql.NullString
}

// CreditDebitRow extends BillRow with credit/debit note fields.
type CreditDebitRow struct {
	BillRow
	NoteText sql.NullString // credit_note.note or debit_note.note
	NoteID   sql.NullInt64  // credit_note.id or debit_note.id
}

// LoadProducts loads products for a given bill from the bill_product table.
func LoadProducts(db *sql.DB, billID int64) ([]ProductRow, error) {
	q := `SELECT p.product_id, p.name, p.price, p.quantity, p.vat, p.discount, p.total_before_vat, p.vat_total, p.total_including_vat FROM bill_product p WHERE p.bill_id = ? ORDER BY p.id`
	rows, err := db.Query(q, billID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []ProductRow
	for rows.Next() {
		var pr ProductRow
		if err := rows.Scan(&pr.ProductID, &pr.Name, &pr.Price, &pr.Quantity, &pr.VAT, &pr.Discount, &pr.TotalBeforeVAT, &pr.VATTotal, &pr.TotalIncludingVAT); err != nil {
			return nil, err
		}
		res = append(res, pr)
	}
	return res, rows.Err()
}

// ParseDates parses effective and payment due dates, returning issueTime.
func ParseDates(effDate, payDue sql.NullString, id int64) (time.Time, error) {
	if effDate.Valid {
		t, err := ParseFlexible(effDate.String)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse effective_date for bill %d: %w", id, err)
		}
		if payDue.Valid {
			if _, err := ParseFlexible(payDue.String); err != nil {
				return time.Time{}, fmt.Errorf("parse payment_due_date for bill %d: %w", id, err)
			}
		}
		return t, nil
	}
	return time.Now(), nil
}

// ParseFlexible tries multiple date formats and returns the first match.
func ParseFlexible(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unsupported date format: %s", s)
}
