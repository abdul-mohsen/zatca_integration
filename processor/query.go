package processor

import (
	"database/sql"
	"fmt"
)

// QueryBillByID loads a single bill row by ID (for daemon consumption).
func QueryBillByID(db *sql.DB, billID int64) (*BillRow, error) {
	q := `SELECT 
		b.id,
		CONCAT('https://ifritah.com/bill/', b.id) AS url,
		b.effective_date,
		b.payment_due_date,
		b.state,
		b.discount,
		b.branch_id,
		company.id as company_id,
		b.total_before_vat,
		b.total_vat,
		b.total,
		b.sequence_number,
		b.merchant_id,
		b.note,
		b.payment_method,
		b.userName,
		b.user_phone_number,
		company.name as company_name,
		company.vat_registration_number,
		store.address_name,
		cl.name as buyer_name,
		cl.company_name as buyer_company,
		cl.vat_number as buyer_vat,
		cl.address as buyer_address,
		cl.street as buyer_street,
		cl.building as buyer_building,
		cl.district as buyer_district,
		cl.city as buyer_city,
		cl.postal_code as buyer_postal_code,
		cl.country as buyer_country,
		cl.scheme_id as buyer_scheme_id,
		cl.registration_id as buyer_registration_id
	FROM bill_totals b
	JOIN store on store.id = b.store_id
	JOIN company on company.id = store.company_id
	LEFT JOIN client cl on cl.id = b.client_id
	WHERE b.id = ?`

	var (
		b           BillRow
		url         sql.NullString
		state       sql.NullInt64
		discount    sql.NullFloat64
		totalBefore sql.NullFloat64
		totalVAT    sql.NullFloat64
		total       sql.NullFloat64
		merchantID  sql.NullInt64
		note        sql.NullString
		userPhone   sql.NullString
		vatReg      sql.NullString
	)
	err := db.QueryRow(q, billID).Scan(
		&b.ID, &url, &b.EffectiveDate, &b.PaymentDue, &state, &discount, &b.BranchID, &b.CompanyID,
		&totalBefore, &totalVAT, &total, &b.SeqNumber, &merchantID, &note, &b.PaymentMethod,
		&b.UserName, &userPhone,
		&b.CompanyName, &vatReg, &b.AddressName,
		&b.BuyerName, &b.BuyerCompany, &b.BuyerVAT,
		&b.BuyerAddress, &b.BuyerStreet, &b.BuyerBuilding, &b.BuyerDistrict,
		&b.BuyerCity, &b.BuyerPostalCode, &b.BuyerCountry,
		&b.BuyerSchemeID, &b.BuyerRegistration,
	)
	if err != nil {
		return nil, fmt.Errorf("query bill %d: %w", billID, err)
	}
	b.URL = url
	return &b, nil
}

// QueryCreditNoteByID loads a credit note row by credit_note.id, JOINing bill.
func QueryCreditNoteByID(db *sql.DB, creditNoteID int64) (*CreditDebitRow, error) {
	q := `SELECT 
		b.id,
		CONCAT('https://ifritah.com/bill/', b.id) AS url,
		b.effective_date,
		b.payment_due_date,
		b.state,
		b.discount,
		b.branch_id,
		company.id as company_id,
		b.total_before_vat,
		b.total_vat,
		b.total,
		b.sequence_number,
		b.merchant_id,
		b.note,
		b.payment_method,
		cred.NOTE as credit_note,
		cred.id as credit_id,
		b.userName,
		b.user_phone_number,
		company.name as company_name,
		company.vat_registration_number,
		store.address_name,
		cl.name as buyer_name,
		cl.company_name as buyer_company,
		cl.vat_number as buyer_vat,
		cl.address as buyer_address,
		cl.street as buyer_street,
		cl.building as buyer_building,
		cl.district as buyer_district,
		cl.city as buyer_city,
		cl.postal_code as buyer_postal_code,
		cl.country as buyer_country,
		cl.scheme_id as buyer_scheme_id,
		cl.registration_id as buyer_registration_id
	FROM credit_note cred
	JOIN bill_totals b on b.id = cred.bill_id
	JOIN store on store.id = b.store_id
	JOIN company on company.id = store.company_id
	LEFT JOIN client cl on cl.id = b.client_id
	WHERE cred.id = ?`

	var (
		row         CreditDebitRow
		url         sql.NullString
		state       sql.NullInt64
		discount    sql.NullFloat64
		totalBefore sql.NullFloat64
		totalVAT    sql.NullFloat64
		total       sql.NullFloat64
		merchantID  sql.NullInt64
		note        sql.NullString
		userPhone   sql.NullString
		vatReg      sql.NullString
	)
	err := db.QueryRow(q, creditNoteID).Scan(
		&row.ID, &url, &row.EffectiveDate, &row.PaymentDue, &state, &discount, &row.BranchID, &row.CompanyID,
		&totalBefore, &totalVAT, &total, &row.SeqNumber, &merchantID, &note, &row.PaymentMethod,
		&row.NoteText, &row.NoteID,
		&row.UserName, &userPhone,
		&row.CompanyName, &vatReg, &row.AddressName,
		&row.BuyerName, &row.BuyerCompany, &row.BuyerVAT,
		&row.BuyerAddress, &row.BuyerStreet, &row.BuyerBuilding, &row.BuyerDistrict,
		&row.BuyerCity, &row.BuyerPostalCode, &row.BuyerCountry,
		&row.BuyerSchemeID, &row.BuyerRegistration,
	)
	if err != nil {
		return nil, fmt.Errorf("query credit note %d: %w", creditNoteID, err)
	}
	row.URL = url
	return &row, nil
}

// QueryDebitNoteByID is kept for compatibility but debit_note table does not exist
// in the current backend schema. This will return an error if called.
func QueryDebitNoteByID(db *sql.DB, debitNoteID int64) (*CreditDebitRow, error) {
	return nil, fmt.Errorf("debit_note table does not exist in the current backend schema")
}

// queryDebitNoteByID_UNUSED is the original implementation preserved for when debit_note table is added.
/*
func queryDebitNoteByID_UNUSED(db *sql.DB, debitNoteID int64) (*CreditDebitRow, error) {
	q := `SELECT
		b.id,
		CONCAT('https://ifritah.com/bill/', b.id) AS url,
		b.effective_date,
		b.payment_due_date,
		b.state,
		b.discount,
		b.branch_id,
		company.id as company_id,
		b.total_before_vat,
		b.total_vat,
		b.total,
		b.sequence_number,
		b.merchant_id,
		b.note,
		b.payment_method,
		deb.note as debit_note,
		deb.id as debit_id,
		b.userName,
		b.user_phone_number,
		company.name as company_name,
		company.vat_registration_number,
		store.address_name,
		cl.name as buyer_name,
		cl.company_name as buyer_company,
		cl.vat_number as buyer_vat,
		cl.address as buyer_address,
		cl.street as buyer_street,
		cl.building as buyer_building,
		cl.district as buyer_district,
		cl.city as buyer_city,
		cl.postal_code as buyer_postal_code,
		cl.country as buyer_country,
		cl.scheme_id as buyer_scheme_id,
		cl.registration_id as buyer_registration_id
	FROM debit_note deb
	JOIN bill_totals b on b.id = deb.bill_id
	JOIN store on store.id = b.store_id
	JOIN company on company.id = store.company_id
	LEFT JOIN client cl on cl.id = b.client_id
	WHERE deb.id = ?`

	var (
		row         CreditDebitRow
		url         sql.NullString
		state       sql.NullInt64
		discount    sql.NullFloat64
		totalBefore sql.NullFloat64
		totalVAT    sql.NullFloat64
		total       sql.NullFloat64
		merchantID  sql.NullInt64
		note        sql.NullString
		userPhone   sql.NullString
		vatReg      sql.NullString
	)
	err := db.QueryRow(q, debitNoteID).Scan(
		&row.ID, &url, &row.EffectiveDate, &row.PaymentDue, &state, &discount, &row.BranchID, &row.CompanyID,
		&totalBefore, &totalVAT, &total, &row.SeqNumber, &merchantID, &note, &row.PaymentMethod,
		&row.NoteText, &row.NoteID,
		&row.UserName, &userPhone,
		&row.CompanyName, &vatReg, &row.AddressName,
		&row.BuyerName, &row.BuyerCompany, &row.BuyerVAT,
		&row.BuyerAddress, &row.BuyerStreet, &row.BuyerBuilding, &row.BuyerDistrict,
		&row.BuyerCity, &row.BuyerPostalCode, &row.BuyerCountry,
		&row.BuyerSchemeID, &row.BuyerRegistration,
	)
	if err != nil {
		return nil, fmt.Errorf("query debit note %d: %w", debitNoteID, err)
	}
	row.URL = url
	return &row, nil
}
*/

// QueryPendingBills returns bill IDs where state < 3 for a given branch (used for startup replay).
func QueryPendingBills(db *sql.DB, branchID int64) ([]int64, error) {
	rows, err := db.Query(`SELECT id FROM bill WHERE branch_id = ? AND state < 3 ORDER BY id`, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// QueryPendingCreditNotes returns credit_note IDs where state = 1 for a given branch.
func QueryPendingCreditNotes(db *sql.DB, branchID int64) ([]int64, error) {
	rows, err := db.Query(`SELECT cred.id FROM credit_note cred JOIN bill b ON b.id = cred.bill_id WHERE b.branch_id = ? AND cred.state = 1 ORDER BY cred.id`, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// QueryPendingDebitNotes is a no-op: debit_note table does not exist in the current backend schema.
func QueryPendingDebitNotes(db *sql.DB, branchID int64) ([]int64, error) {
	return nil, nil
}

// QueryActiveBranches returns all active branch IDs for a given tenant database.
func QueryActiveBranches(db *sql.DB) ([]int64, error) {
	rows, err := db.Query(`SELECT b.id FROM branches b JOIN company c ON c.id = b.company_id JOIN branch_zatca_config bz ON bz.branch_id = b.id WHERE b.is_active = 1 AND COALESCE(bz.zatca_production_username, '') != '' ORDER BY b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
