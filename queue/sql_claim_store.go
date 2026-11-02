package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	claimProcessing   = "processing"
	claimCompleted    = "completed"
	defaultClaimLease = 30 * time.Minute
)

// SQLClaimStore keeps one claim per request in the tenant database. The table zatca_consumer_claim is owned by the backend
// (goose 00003). A request is unique by (pos_id, bill_request_id), and an event by event_id.
type SQLClaimStore struct {
	DB            *sql.DB
	LeaseDuration time.Duration
	Now           func() time.Time
}

// NewSQLClaimStore returns a claim store for one tenant database.
func NewSQLClaimStore(db *sql.DB) *SQLClaimStore {
	return &SQLClaimStore{DB: db, LeaseDuration: defaultClaimLease}
}

func (s *SQLClaimStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *SQLClaimStore) leaseDuration() time.Duration {
	if s.LeaseDuration <= 0 {
		return defaultClaimLease
	}
	return s.LeaseDuration
}

// Claim decides what a delivery does with its request. A new request is inserted as processing, a completed one is a
// duplicate, and one with a live lease is in flight. A request whose fingerprint differs from the stored one conflicts.
func (s *SQLClaimStore) Claim(ctx context.Context, msg Message) (ClaimDecision, error) {
	if err := msg.Validate(); err != nil {
		return ClaimConflict, err
	}
	if s == nil || s.DB == nil {
		return ClaimConflict, errors.New("queue SQL claim store is not configured")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ClaimConflict, fmt.Errorf("begin queue claim: %w", err)
	}
	defer tx.Rollback()

	decision, err := s.claimInTx(ctx, tx, msg)
	if err != nil {
		return ClaimConflict, err
	}
	if err := tx.Commit(); err != nil {
		return ClaimConflict, fmt.Errorf("commit queue claim %s: %w", msg.BillRequestID, err)
	}
	return decision, nil
}

func (s *SQLClaimStore) claimInTx(ctx context.Context, tx *sql.Tx, msg Message) (ClaimDecision, error) {
	var (
		fingerprint  string
		state        string
		leaseExpires nullableTime
	)
	err := tx.QueryRowContext(ctx, `
		SELECT fingerprint, state, lease_expires_at
		FROM zatca_consumer_claim
		WHERE pos_id = ? AND bill_request_id = ?
		FOR UPDATE
	`, msg.PosID, msg.BillRequestID).Scan(&fingerprint, &state, &leaseExpires)
	if errors.Is(err, sql.ErrNoRows) {
		return s.insertClaim(ctx, tx, msg)
	}
	if err != nil {
		return ClaimConflict, fmt.Errorf("lookup queue claim %s: %w", msg.BillRequestID, err)
	}
	if fingerprint != msg.RequestFingerprint {
		return ClaimConflict, nil
	}
	now := s.now()
	decision := sqlClaimDecision(state, leaseExpires, now)
	if decision != ClaimProcess {
		return decision, nil
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE zatca_consumer_claim
		SET event_id = ?, egs_sequence_id = ?, state = 'processing', lease_expires_at = ?
		WHERE pos_id = ? AND bill_request_id = ?
	`, msg.EventID, msg.EGSSequenceID, now.Add(s.leaseDuration()), msg.PosID, msg.BillRequestID)
	if err != nil {
		return ClaimConflict, fmt.Errorf("reclaim queue request %s: %w", msg.BillRequestID, err)
	}
	return ClaimProcess, nil
}

func (s *SQLClaimStore) insertClaim(ctx context.Context, tx *sql.Tx, msg Message) (ClaimDecision, error) {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO zatca_consumer_claim
			(pos_id, bill_request_id, event_id, egs_sequence_id, fingerprint, state, lease_expires_at)
		VALUES (?, ?, ?, ?, ?, 'processing', ?)
	`, msg.PosID, msg.BillRequestID, msg.EventID, msg.EGSSequenceID, msg.RequestFingerprint,
		s.now().Add(s.leaseDuration()))
	if isMySQLError(err, mysqlDuplicateKey) {
		// The event ID already belongs to another request.
		return ClaimConflict, nil
	}
	if err != nil {
		return ClaimConflict, fmt.Errorf("insert queue claim %s: %w", msg.BillRequestID, err)
	}
	return ClaimProcess, nil
}

func sqlClaimDecision(state string, lease nullableTime, now time.Time) ClaimDecision {
	switch {
	case state == claimCompleted:
		return ClaimDuplicate
	case lease.Valid && lease.Time.After(now):
		return ClaimInFlight
	default:
		return ClaimProcess
	}
}

// Complete marks the request done. It is safe to repeat.
func (s *SQLClaimStore) Complete(ctx context.Context, msg Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	if s == nil || s.DB == nil {
		return errors.New("queue SQL claim store is not configured")
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE zatca_consumer_claim
		SET state = 'completed', lease_expires_at = NULL, completed_at = CURRENT_TIMESTAMP
		WHERE pos_id = ? AND bill_request_id = ? AND fingerprint = ? AND state = 'processing'
	`, msg.PosID, msg.BillRequestID, msg.RequestFingerprint)
	if err != nil {
		return fmt.Errorf("complete queue claim %s: %w", msg.BillRequestID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect queue claim %s completion: %w", msg.BillRequestID, err)
	}
	if rows > 0 {
		return nil
	}

	var state string
	err = s.DB.QueryRowContext(ctx, `
		SELECT state
		FROM zatca_consumer_claim
		WHERE pos_id = ? AND bill_request_id = ? AND fingerprint = ?
	`, msg.PosID, msg.BillRequestID, msg.RequestFingerprint).Scan(&state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: %s", ErrClaimNotFound, msg.BillRequestID)
	case err != nil:
		return fmt.Errorf("inspect queue claim %s: %w", msg.BillRequestID, err)
	case state == claimCompleted:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrClaimInFlight, msg.BillRequestID)
	}
}
