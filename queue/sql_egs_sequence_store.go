package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// SQLSequenceStore persists the canonical backend EGS chain contract. The
// backend owns the egs_sequence, zatca_egs_document, and outbox schema; the
// daemon only reserves the stream and commits the immutable signed snapshot.
type SQLSequenceStore struct {
	DB            *sql.DB
	LeaseDuration time.Duration
	Now           func() time.Time
}

type EGSSequenceAllocator = SQLSequenceStore

// NewSQLSequenceStore creates a chain store backed by the backend contract.
func NewSQLSequenceStore(db *sql.DB) *SQLSequenceStore {
	return &SQLSequenceStore{
		DB:            db,
		LeaseDuration: defaultEGSSequenceLease,
	}
}

// NewEGSSequenceAllocator is the backend-contract name retained for callers
// that share chain terminology with the backend package.
func NewEGSSequenceAllocator(db *sql.DB) *SQLSequenceStore {
	return NewSQLSequenceStore(db)
}

func (s *SQLSequenceStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *SQLSequenceStore) leaseDuration(request time.Duration) time.Duration {
	if request > 0 {
		return request
	}
	if s.LeaseDuration > 0 {
		return s.LeaseDuration
	}
	return defaultEGSSequenceLease
}

// Reserve claims the next ICV and the previous committed hash under a
// tenant/EGS row lock. Replaying a committed document is reported as
// Committed, allowing the queue ACK protocol to skip the provider effect.
func (s *SQLSequenceStore) Reserve(
	ctx context.Context,
	request EGSSequenceReservationRequest,
) (EGSSequenceReservation, error) {
	if err := validateEGSReservationRequest(request); err != nil {
		return EGSSequenceReservation{}, err
	}
	if s == nil || s.DB == nil {
		return EGSSequenceReservation{}, errors.New("SQL EGS sequence store is not configured")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return EGSSequenceReservation{}, fmt.Errorf("begin EGS sequence reservation: %w", err)
	}
	defer tx.Rollback()

	committed, err := loadCommittedDocument(ctx, tx, request)
	if err != nil {
		return EGSSequenceReservation{}, err
	}
	if committed != nil {
		if committed.BranchID != request.BranchID ||
			committed.VATNumber != request.VATNumber ||
			committed.EGSID != request.EGSID ||
			committed.OutboxEventID != request.OutboxEventID {
			return EGSSequenceReservation{}, fmt.Errorf("%w: document %s", ErrEGSSequenceOwnership, request.DocumentUUID)
		}
		if err := tx.Commit(); err != nil {
			return EGSSequenceReservation{}, fmt.Errorf("commit EGS sequence replay lookup: %w", err)
		}
		return EGSSequenceReservation{
			EGSSequenceIdentity: EGSSequenceIdentity{
				EGSSequenceID: committed.EGSSequenceID,
				TenantID:      committed.TenantID,
				BranchID:      committed.BranchID,
				VATNumber:     committed.VATNumber,
				EGSID:         committed.EGSID,
			},
			ReservationKey:   request.ReservationKey,
			ReservationToken: request.ReservationToken,
			DocumentUUID:     committed.DocumentUUID,
			OutboxEventID:    committed.OutboxEventID,
			ICV:              committed.ICV,
			PIH:              cloneStringPointer(committed.PIH),
			Committed:        true,
		}, nil
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO egs_sequence
			(egs_sequence_id, tenant_id, branch_id, vat_number, egs_id)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE egs_sequence_id = egs_sequence_id
	`, request.EGSSequenceID, request.TenantID, request.BranchID, request.VATNumber, request.EGSID)
	if err != nil {
		return EGSSequenceReservation{}, fmt.Errorf("ensure EGS sequence %s: %w", request.EGSSequenceID, err)
	}

	state, err := scanSQLSequenceState(tx.QueryRowContext(ctx, `
		SELECT
			egs_sequence_id, tenant_id, branch_id, vat_number, egs_id,
			last_icv, previous_invoice_hash, reservation_key, reservation_token,
			reserved_icv, reserved_previous_invoice_hash, reserved_document_uuid,
			reserved_outbox_event_id, lease_expires_at
		FROM egs_sequence
		WHERE egs_sequence_id = ?
		  AND tenant_id = ?
		  AND branch_id = ?
		  AND vat_number = ?
		  AND egs_id = ?
		FOR UPDATE
	`, request.EGSSequenceID, request.TenantID, request.BranchID, request.VATNumber, request.EGSID))
	if errors.Is(err, sql.ErrNoRows) {
		return EGSSequenceReservation{}, fmt.Errorf("%w: %s", ErrEGSSequenceOwnership, request.EGSSequenceID)
	}
	if err != nil {
		return EGSSequenceReservation{}, fmt.Errorf("lock EGS sequence %s: %w", request.EGSSequenceID, err)
	}

	now := s.now()
	if state.reservationKey.Valid {
		if state.reservationKey.String == request.ReservationKey {
			if (state.reservedDocumentUUID.Valid && state.reservedDocumentUUID.String != request.DocumentUUID) ||
				(state.reservedOutboxEventID.Valid && state.reservedOutboxEventID.String != request.OutboxEventID) {
				return EGSSequenceReservation{}, fmt.Errorf("%w: %s", ErrEGSSequenceReservationConflict, request.ReservationKey)
			}
			reservation, err := state.reservation()
			if err != nil {
				return EGSSequenceReservation{}, err
			}
			if err := tx.Commit(); err != nil {
				return EGSSequenceReservation{}, fmt.Errorf("commit EGS sequence reservation replay: %w", err)
			}
			return reservation, nil
		}
		if state.leaseExpiresAt.Valid && state.leaseExpiresAt.Time.After(now) {
			return EGSSequenceReservation{}, fmt.Errorf("%w: %s", ErrEGSSequenceBusy, request.EGSSequenceID)
		}
	}

	leaseExpiresAt := now.Add(s.leaseDuration(request.LeaseDuration))
	result, err := tx.ExecContext(ctx, `
		UPDATE egs_sequence
		SET
			reservation_key = ?,
			reservation_token = ?,
			reserved_icv = last_icv + 1,
			reserved_previous_invoice_hash = previous_invoice_hash,
			reserved_document_uuid = ?,
			reserved_outbox_event_id = ?,
			reserved_at = CURRENT_TIMESTAMP,
			lease_expires_at = ?
		WHERE egs_sequence_id = ?
		  AND tenant_id = ?
		  AND branch_id = ?
		  AND vat_number = ?
		  AND egs_id = ?
		  AND (
		    reservation_key IS NULL
		    OR lease_expires_at IS NULL
		    OR lease_expires_at <= CURRENT_TIMESTAMP
		  )
	`, request.ReservationKey, request.ReservationToken, request.DocumentUUID, request.OutboxEventID,
		leaseExpiresAt, request.EGSSequenceID, request.TenantID, request.BranchID, request.VATNumber, request.EGSID)
	if err != nil {
		return EGSSequenceReservation{}, fmt.Errorf("reserve EGS sequence %s: %w", request.EGSSequenceID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return EGSSequenceReservation{}, fmt.Errorf("inspect EGS sequence reservation %s: %w", request.EGSSequenceID, err)
	}
	if affected != 1 {
		return EGSSequenceReservation{}, fmt.Errorf("%w: %s", ErrEGSSequenceBusy, request.EGSSequenceID)
	}

	reservation := EGSSequenceReservation{
		EGSSequenceIdentity: request.EGSSequenceIdentity,
		ReservationKey:      request.ReservationKey,
		ReservationToken:    request.ReservationToken,
		DocumentUUID:        request.DocumentUUID,
		OutboxEventID:       request.OutboxEventID,
		ICV:                 state.lastICV + 1,
		PIH:                 cloneStringPointer(state.previousInvoiceHash),
		LeaseExpiresAt:      leaseExpiresAt,
	}
	if err := tx.Commit(); err != nil {
		return EGSSequenceReservation{}, fmt.Errorf("commit EGS sequence reservation %s: %w", request.EGSSequenceID, err)
	}
	return reservation, nil
}

// Commit inserts the immutable document, binds the matching backend outbox
// event to its reserved ICV/PIH, and advances the chain in one transaction.
func (s *SQLSequenceStore) Commit(ctx context.Context, request EGSSequenceCommitRequest) error {
	if err := validateEGSCommitRequest(request); err != nil {
		return err
	}
	if s == nil || s.DB == nil {
		return errors.New("SQL EGS sequence store is not configured")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin EGS sequence commit: %w", err)
	}
	defer tx.Rollback()

	existing, err := loadCommittedDocument(ctx, tx, EGSSequenceReservationRequest{
		EGSSequenceIdentity: request.Reservation.EGSSequenceIdentity,
		DocumentUUID:        request.Reservation.DocumentUUID,
		OutboxEventID:       request.Reservation.OutboxEventID,
	})
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.BranchID != request.Reservation.BranchID ||
			existing.VATNumber != request.Reservation.VATNumber ||
			existing.EGSID != request.Reservation.EGSID {
			return fmt.Errorf("%w: document %s", ErrEGSSequenceOwnership, request.Reservation.DocumentUUID)
		}
		if existing.DocumentType != request.DocumentType ||
			existing.SourceID != request.SourceID ||
			existing.OriginalDocumentType != request.OriginalDocumentType ||
			existing.OriginalSourceID != request.OriginalSourceID ||
			existing.OriginalInvoiceUUID != request.OriginalInvoiceUUID ||
			existing.ICV != request.Reservation.ICV ||
			!equalStringPointer(existing.PIH, request.Reservation.PIH) ||
			existing.OutboxEventID != request.Reservation.OutboxEventID ||
			string(existing.Snapshot) != string(request.Snapshot) {
			return fmt.Errorf("%w: immutable document %s changed", ErrEGSSequenceReservationConflict, request.Reservation.DocumentUUID)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit existing EGS document %s: %w", request.Reservation.DocumentUUID, err)
		}
		return nil
	}

	state := request.State
	if state == "" {
		state = EGSDocumentQueued
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO zatca_egs_document (
			tenant_id, egs_sequence_id, branch_id, vat_number, egs_id,
			document_uuid, document_type, source_id,
			original_document_type, original_source_id, original_invoice_uuid,
			outbox_event_id, icv, pih, document_snapshot, state
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, request.Reservation.TenantID, request.Reservation.EGSSequenceID, request.Reservation.BranchID,
		request.Reservation.VATNumber, request.Reservation.EGSID, request.Reservation.DocumentUUID,
		request.DocumentType, request.SourceID,
		optionalString(request.OriginalDocumentType), optionalUint64(request.OriginalSourceID),
		optionalString(request.OriginalInvoiceUUID), request.Reservation.OutboxEventID,
		request.Reservation.ICV, request.Reservation.PIH, request.Snapshot, state)
	if err != nil {
		return fmt.Errorf("insert immutable EGS document %s: %w", request.Reservation.DocumentUUID, err)
	}

	var (
		outboxSequence sql.NullString
		outboxUUID     sql.NullString
		outboxICV      nullableUint64
		outboxPIH      sql.NullString
	)
	err = tx.QueryRowContext(ctx, `
		SELECT egs_sequence_id, invoice_uuid, icv, pih
		FROM outbox
		WHERE tenant_id = ? AND event_id = ?
		FOR UPDATE
	`, request.Reservation.TenantID, request.Reservation.OutboxEventID).Scan(
		&outboxSequence, &outboxUUID, &outboxICV, &outboxPIH,
	)
	if err != nil {
		return fmt.Errorf("load backend outbox event %s: %w", request.Reservation.OutboxEventID, err)
	}
	if outboxSequence.Valid && outboxSequence.String != request.Reservation.EGSSequenceID {
		return fmt.Errorf("%w: outbox EGS sequence %s", ErrEGSSequenceOwnership, request.Reservation.OutboxEventID)
	}
	if outboxUUID.Valid && outboxUUID.String != request.Reservation.DocumentUUID {
		return fmt.Errorf("%w: outbox document UUID %s", ErrEGSSequenceOwnership, request.Reservation.OutboxEventID)
	}
	if outboxICV.Valid && outboxICV.Value != request.Reservation.ICV {
		return fmt.Errorf("%w: outbox ICV %s", ErrEGSSequenceOwnership, request.Reservation.OutboxEventID)
	}
	if outboxPIH.Valid && request.Reservation.PIH != nil && outboxPIH.String != *request.Reservation.PIH {
		return fmt.Errorf("%w: outbox PIH %s", ErrEGSSequenceOwnership, request.Reservation.OutboxEventID)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE outbox
		SET egs_sequence_id = ?, invoice_uuid = ?, icv = ?, pih = ?
		WHERE tenant_id = ? AND event_id = ?
	`, request.Reservation.EGSSequenceID, request.Reservation.DocumentUUID, request.Reservation.ICV,
		request.Reservation.PIH, request.Reservation.TenantID, request.Reservation.OutboxEventID); err != nil {
		return fmt.Errorf("bind backend outbox event %s: %w", request.Reservation.OutboxEventID, err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE egs_sequence AS s
		SET
			last_icv = s.reserved_icv,
			previous_invoice_hash = ?,
			reservation_key = NULL,
			reservation_token = NULL,
			reserved_icv = NULL,
			reserved_previous_invoice_hash = NULL,
			reserved_document_uuid = NULL,
			reserved_outbox_event_id = NULL,
			reserved_at = NULL,
			lease_expires_at = NULL
		WHERE s.egs_sequence_id = ?
		  AND s.tenant_id = ?
		  AND s.branch_id = ?
		  AND s.vat_number = ?
		  AND s.egs_id = ?
		  AND s.reservation_key = ?
		  AND s.reservation_token = ?
		  AND s.reserved_icv = ?
		  AND s.reserved_document_uuid = ?
		  AND s.reserved_outbox_event_id = ?
		  AND EXISTS (
		    SELECT 1
		    FROM zatca_egs_document AS d
		    JOIN outbox AS o
		      ON o.tenant_id = d.tenant_id
		     AND o.event_id = d.outbox_event_id
		     AND o.egs_sequence_id = d.egs_sequence_id
		     AND o.invoice_uuid = d.document_uuid
		     AND o.icv = d.icv
		    WHERE d.tenant_id = s.tenant_id
		      AND d.egs_sequence_id = s.egs_sequence_id
		      AND d.branch_id = s.branch_id
		      AND d.vat_number = s.vat_number
		      AND d.egs_id = s.egs_id
		      AND d.document_uuid = s.reserved_document_uuid
		      AND d.outbox_event_id = s.reserved_outbox_event_id
		      AND d.icv = s.reserved_icv
		      AND d.state NOT IN ('rejected', 'ambiguous', 'terminal')
		  )
	`, request.DocumentHash, request.Reservation.EGSSequenceID, request.Reservation.TenantID,
		request.Reservation.BranchID, request.Reservation.VATNumber, request.Reservation.EGSID,
		request.Reservation.ReservationKey, request.Reservation.ReservationToken, request.Reservation.ICV,
		request.Reservation.DocumentUUID, request.Reservation.OutboxEventID)
	if err != nil {
		return fmt.Errorf("commit EGS sequence %s: %w", request.Reservation.EGSSequenceID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect EGS sequence commit %s: %w", request.Reservation.EGSSequenceID, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s", ErrEGSSequenceCommitNotApplied, request.Reservation.EGSSequenceID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit EGS document and sequence %s: %w", request.Reservation.EGSSequenceID, err)
	}
	return nil
}

// Release clears an uncommitted reservation when an operator explicitly
// abandons a document. Normal processing leaves the lease for crash recovery.
func (s *SQLSequenceStore) Release(ctx context.Context, reservation EGSSequenceReservation) error {
	if err := validateEGSReservation(reservation); err != nil {
		return err
	}
	if s == nil || s.DB == nil {
		return errors.New("SQL EGS sequence store is not configured")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin EGS sequence release: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE egs_sequence
		SET
			reservation_key = NULL,
			reservation_token = NULL,
			reserved_icv = NULL,
			reserved_previous_invoice_hash = NULL,
			reserved_document_uuid = NULL,
			reserved_outbox_event_id = NULL,
			reserved_at = NULL,
			lease_expires_at = NULL
		WHERE egs_sequence_id = ?
		  AND tenant_id = ?
		  AND branch_id = ?
		  AND vat_number = ?
		  AND egs_id = ?
		  AND reservation_key = ?
		  AND reservation_token = ?
	`, reservation.EGSSequenceID, reservation.TenantID, reservation.BranchID, reservation.VATNumber,
		reservation.EGSID, reservation.ReservationKey, reservation.ReservationToken)
	if err != nil {
		return fmt.Errorf("release EGS sequence %s: %w", reservation.EGSSequenceID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect EGS sequence release %s: %w", reservation.EGSSequenceID, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s", ErrEGSSequenceOwnership, reservation.EGSSequenceID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit EGS sequence release %s: %w", reservation.EGSSequenceID, err)
	}
	return nil
}

type sqlEGSSequenceState struct {
	identity                    EGSSequenceIdentity
	lastICV                     uint64
	previousInvoiceHash         *string
	reservationKey              sql.NullString
	reservationToken            sql.NullString
	reservedICV                 nullableUint64
	reservedPreviousInvoiceHash sql.NullString
	reservedDocumentUUID        sql.NullString
	reservedOutboxEventID       sql.NullString
	leaseExpiresAt              nullableClaimTime
}

func scanSQLSequenceState(row *sql.Row) (sqlEGSSequenceState, error) {
	var (
		state        sqlEGSSequenceState
		lastICV      nullableUint64
		previousHash sql.NullString
	)
	err := row.Scan(
		&state.identity.EGSSequenceID,
		&state.identity.TenantID,
		&state.identity.BranchID,
		&state.identity.VATNumber,
		&state.identity.EGSID,
		&lastICV,
		&previousHash,
		&state.reservationKey,
		&state.reservationToken,
		&state.reservedICV,
		&state.reservedPreviousInvoiceHash,
		&state.reservedDocumentUUID,
		&state.reservedOutboxEventID,
		&state.leaseExpiresAt,
	)
	if err != nil {
		return sqlEGSSequenceState{}, err
	}
	state.lastICV = lastICV.Value
	if previousHash.Valid {
		state.previousInvoiceHash = cloneStringPointer(&previousHash.String)
	}
	return state, nil
}

func (s sqlEGSSequenceState) reservation() (EGSSequenceReservation, error) {
	if !s.reservedICV.Valid ||
		!s.reservationKey.Valid ||
		!s.reservationToken.Valid ||
		!s.reservedDocumentUUID.Valid ||
		!s.reservedOutboxEventID.Valid ||
		!s.leaseExpiresAt.Valid {
		return EGSSequenceReservation{}, fmt.Errorf("%w: incomplete reservation for %s", ErrEGSSequenceOwnership, s.identity.EGSSequenceID)
	}
	return EGSSequenceReservation{
		EGSSequenceIdentity: s.identity,
		ReservationKey:      s.reservationKey.String,
		ReservationToken:    s.reservationToken.String,
		DocumentUUID:        s.reservedDocumentUUID.String,
		OutboxEventID:       s.reservedOutboxEventID.String,
		ICV:                 s.reservedICV.Value,
		PIH:                 cloneStringPointer(nullableStringPointer(s.reservedPreviousInvoiceHash)),
		LeaseExpiresAt:      s.leaseExpiresAt.Time,
	}, nil
}

func loadCommittedDocument(
	ctx context.Context,
	tx *sql.Tx,
	request EGSSequenceReservationRequest,
) (*EGSSequenceDocument, error) {
	var (
		document             EGSSequenceDocument
		originalDocumentType sql.NullString
		originalSourceID     nullableUint64
		originalInvoiceUUID  sql.NullString
		pih                  sql.NullString
		snapshot             []byte
	)
	err := tx.QueryRowContext(ctx, `
		SELECT
			egs_sequence_id, tenant_id, branch_id, vat_number, egs_id,
			document_uuid, document_type, source_id,
			original_document_type, original_source_id, original_invoice_uuid,
			outbox_event_id,
			icv, pih, document_snapshot, state
		FROM zatca_egs_document
		WHERE tenant_id = ?
		  AND egs_sequence_id = ?
		  AND document_uuid = ?
		FOR UPDATE
	`, request.TenantID, request.EGSSequenceID, request.DocumentUUID).Scan(
		&document.EGSSequenceID,
		&document.TenantID,
		&document.BranchID,
		&document.VATNumber,
		&document.EGSID,
		&document.DocumentUUID,
		&document.DocumentType,
		&document.SourceID,
		&originalDocumentType,
		&originalSourceID,
		&originalInvoiceUUID,
		&document.OutboxEventID,
		&document.ICV,
		&pih,
		&snapshot,
		&document.State,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load EGS document %s: %w", request.DocumentUUID, err)
	}
	if document.OutboxEventID != request.OutboxEventID {
		return nil, fmt.Errorf("%w: document %s", ErrEGSSequenceReservationConflict, request.DocumentUUID)
	}
	document.PIH = nullableStringPointer(pih)
	document.OriginalDocumentType = originalDocumentType.String
	document.OriginalSourceID = originalSourceID.Value
	document.OriginalInvoiceUUID = originalInvoiceUUID.String
	document.Snapshot = append([]byte(nil), snapshot...)
	return &document, nil
}

type nullableUint64 struct {
	Value uint64
	Valid bool
}

func (v *nullableUint64) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		v.Value = 0
		v.Valid = false
		return nil
	case uint64:
		v.Value = typed
	case uint32:
		v.Value = uint64(typed)
	case int64:
		if typed < 0 {
			return fmt.Errorf("negative unsigned integer %d", typed)
		}
		v.Value = uint64(typed)
	case []byte:
		parsed, err := strconv.ParseUint(string(typed), 10, 64)
		if err != nil {
			return err
		}
		v.Value = parsed
	case string:
		parsed, err := strconv.ParseUint(typed, 10, 64)
		if err != nil {
			return err
		}
		v.Value = parsed
	default:
		return fmt.Errorf("unsupported unsigned integer %T", value)
	}
	v.Valid = true
	return nil
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}
