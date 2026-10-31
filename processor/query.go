package processor

import (
	"database/sql"
	"fmt"

	"github.com/zatca-go/zatca/queue"
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

// QueryCreditNoteByID loads a credit note and its canonical source contract.
func QueryCreditNoteByID(db *sql.DB, creditNoteID int64) (*CreditDebitRow, error) {
	return queryNoteByID(db, "credit", creditNoteID)
}

// QueryDebitNoteByID loads a debit note and its canonical source contract.
func QueryDebitNoteByID(db *sql.DB, debitNoteID int64) (*CreditDebitRow, error) {
	return queryNoteByID(db, "debit", debitNoteID)
}

func queryNoteByID(db *sql.DB, documentType string, noteID int64) (*CreditDebitRow, error) {
	if documentType != "credit" && documentType != "debit" {
		return nil, fmt.Errorf("unsupported note document type %q", documentType)
	}

	noteTable := "credit_note"
	noteAlias := "cred"
	if documentType == "debit" {
		noteTable = "debit_note"
		noteAlias = "deb"
	}

	q := fmt.Sprintf(`SELECT
		b.id,
		CONCAT('https://ifritah.com/bill/', b.id) AS url,
		b.effective_date,
		b.payment_due_date,
		b.state,
		b.discount,
		ds.branch_id,
		company.id as company_id,
		b.total_before_vat,
		b.total_vat,
		b.total,
		ds.sequence_number,
		b.merchant_id,
		b.note,
		b.payment_method,
		%s.note as note_reason,
		%s.id as note_id,
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
		cl.registration_id as buyer_registration_id,
		ds.tenant_id,
		ds.document_type,
		ds.source_id,
		ds.pos_id,
		ds.request_id,
		ds.invoice_uuid,
		ds.original_document_type,
		ds.original_source_id,
		ds.original_invoice_uuid
	FROM %s %s
	JOIN bill_totals b ON b.id = %s.bill_id
	JOIN store ON store.id = b.store_id
	JOIN company ON company.id = store.company_id
	LEFT JOIN client cl ON cl.id = b.client_id
	JOIN zatca_document_source ds
	  ON ds.document_type = ?
	 AND ds.source_id = %s.id
	WHERE %s.id = ?`, noteAlias, noteAlias, noteTable, noteAlias, noteAlias, noteAlias, noteAlias)

	var (
		row                                              CreditDebitRow
		url                                              sql.NullString
		state                                            sql.NullInt64
		discount                                         sql.NullFloat64
		totalBefore                                      sql.NullFloat64
		totalVAT                                         sql.NullFloat64
		total                                            sql.NullFloat64
		merchantID                                       sql.NullInt64
		note                                             sql.NullString
		userPhone                                        sql.NullString
		vatReg                                           sql.NullString
		source                                           DocumentSourceRow
		sourceTenant, sourceID, originalSourceID         sql.NullInt64
		sourceType, sourcePos, sourceRequest, sourceUUID sql.NullString
		sourceOriginalType, sourceOriginalUUID           sql.NullString
	)
	err := db.QueryRow(q, documentType, noteID).Scan(
		&row.ID, &url, &row.EffectiveDate, &row.PaymentDue, &state, &discount, &row.BranchID, &row.CompanyID,
		&totalBefore, &totalVAT, &total, &row.SeqNumber, &merchantID, &note, &row.PaymentMethod,
		&row.NoteText, &row.NoteID,
		&row.UserName, &userPhone,
		&row.CompanyName, &vatReg, &row.AddressName,
		&row.BuyerName, &row.BuyerCompany, &row.BuyerVAT,
		&row.BuyerAddress, &row.BuyerStreet, &row.BuyerBuilding, &row.BuyerDistrict,
		&row.BuyerCity, &row.BuyerPostalCode, &row.BuyerCountry,
		&row.BuyerSchemeID, &row.BuyerRegistration,
		&sourceTenant, &sourceType, &sourceID, &sourcePos, &sourceRequest, &sourceUUID,
		&sourceOriginalType, &originalSourceID, &sourceOriginalUUID,
	)
	if err != nil {
		return nil, fmt.Errorf("query %s note %d: %w", documentType, noteID, err)
	}
	if !sourceType.Valid || sourceType.String != documentType {
		return nil, fmt.Errorf("canonical source for %s note %d has document_type %q",
			documentType, noteID, sourceType.String)
	}
	if !sourceID.Valid || sourceID.Int64 != noteID {
		return nil, fmt.Errorf("canonical source for %s note %d has source_id %d",
			documentType, noteID, sourceID.Int64)
	}

	source = DocumentSourceRow{
		DocumentType:         sourceType.String,
		SourceID:             sourceID.Int64,
		PosID:                sourcePos.String,
		RequestID:            sourceRequest.String,
		InvoiceUUID:          sourceUUID.String,
		BranchID:             row.BranchID,
		SequenceNumber:       row.SeqNumber.String,
		OriginalDocumentType: sourceOriginalType.String,
		OriginalSourceID:     originalSourceID.Int64,
		OriginalInvoiceUUID:  sourceOriginalUUID.String,
	}
	if sourceTenant.Valid && sourceTenant.Int64 > 0 {
		source.TenantID = uint64(sourceTenant.Int64)
	}
	row.Source = source
	row.URL = url
	return &row, nil
}

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

// QueryPendingCreditNotes returns canonical credit-note source IDs pending submission.
func QueryPendingCreditNotes(db *sql.DB, branchID int64) ([]int64, error) {
	return queryPendingNotes(db, "credit", "credit_note", branchID)
}

// QueryPendingDebitNotes returns canonical debit-note source IDs pending submission.
func QueryPendingDebitNotes(db *sql.DB, branchID int64) ([]int64, error) {
	return queryPendingNotes(db, "debit", "debit_note", branchID)
}

func queryPendingNotes(db *sql.DB, documentType, noteTable string, branchID int64) ([]int64, error) {
	q := fmt.Sprintf(`SELECT ds.source_id
		FROM zatca_document_source ds
		JOIN %s n ON n.id = ds.source_id
		WHERE ds.document_type = ? AND ds.branch_id = ? AND n.state = 1
		ORDER BY ds.source_id`, noteTable)
	rows, err := db.Query(q, documentType, branchID)
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

// QueryPendingOrderedMessages returns canonical queue envelopes from the
// backend outbox. Replay must not reconstruct document-type messages from
// source tables because that would lose the immutable UUID/EGS identity.
func QueryPendingOrderedMessages(db *sql.DB, tenantID uint64, branchID int64) ([]queue.Message, error) {
	rows, err := db.Query(`
		SELECT payload
		FROM outbox
		WHERE tenant_id = ?
		  AND egs_sequence_id IS NOT NULL
		  AND invoice_uuid IS NOT NULL
		  AND status IN ('pending', 'retryable', 'in_flight', 'published')
		ORDER BY id
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []queue.Message
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		msg, err := queue.DecodeCanonicalEnvelope(payload)
		if err != nil {
			return nil, fmt.Errorf("decode ordered replay payload: %w", err)
		}
		if msg.BranchID != branchID {
			continue
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
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
