package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const defaultClaimLease = 30 * time.Minute

// SQLClaimStore persists one business-effect claim per canonical request.
// The request and event unique keys are both tenant-scoped.
type SQLClaimStore struct {
	DB            *sql.DB
	LeaseDuration time.Duration
	Now           func() time.Time
}

type nullableClaimTime struct {
	time.Time
	Valid bool
}

func (t *nullableClaimTime) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		t.Valid = false
		t.Time = time.Time{}
		return nil
	case time.Time:
		t.Valid = true
		t.Time = typed
		return nil
	case []byte:
		return t.parse(string(typed))
	case string:
		return t.parse(typed)
	default:
		return fmt.Errorf("unsupported claim lease time %T", value)
	}
}

func (t *nullableClaimTime) parse(value string) error {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			t.Valid = true
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("invalid claim lease time %q", value)
}

// NewSQLClaimStore creates a durable claim store backed by a tenant database.
func NewSQLClaimStore(db *sql.DB) *SQLClaimStore {
	return &SQLClaimStore{
		DB:            db,
		LeaseDuration: defaultClaimLease,
	}
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

// Claim atomically consults the event and request identities, creating or
// reclaiming the request claim when the previous lease is absent or expired.
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

	now := s.now()
	leaseExpires := now.Add(s.leaseDuration())
	fingerprint := msg.BusinessFingerprint()

	var event struct {
		posID         string
		billRequestID string
		fingerprint   string
		state         string
		leaseExpires  nullableClaimTime
	}
	err = tx.QueryRowContext(ctx, `
		SELECT pos_id, bill_request_id, fingerprint, state, lease_expires_at
		FROM zatca_consumer_claim
		WHERE tenant_id = ? AND event_id = ?
		FOR UPDATE
	`, msg.TenantID, msg.EventID).Scan(
		&event.posID,
		&event.billRequestID,
		&event.fingerprint,
		&event.state,
		&event.leaseExpires,
	)
	switch {
	case err == nil:
		if event.posID != msg.PosID ||
			event.billRequestID != msg.BillRequestID ||
			event.fingerprint != fingerprint {
			if err := tx.Commit(); err != nil {
				return ClaimConflict, fmt.Errorf("commit queue conflict lookup: %w", err)
			}
			return ClaimConflict, nil
		}
		decision := sqlClaimDecision(event.state, event.leaseExpires, now)
		if decision == ClaimProcess {
			if _, err := tx.ExecContext(ctx, `
				UPDATE zatca_consumer_claim
				SET state = 'processing', lease_expires_at = ?, updated_at = CURRENT_TIMESTAMP
				WHERE tenant_id = ? AND event_id = ?
			`, leaseExpires, msg.TenantID, msg.EventID); err != nil {
				return ClaimConflict, fmt.Errorf("reclaim queue event %s: %w", msg.EventID, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return ClaimConflict, fmt.Errorf("commit queue event %s: %w", msg.EventID, err)
		}
		return decision, nil
	case !errors.Is(err, sql.ErrNoRows):
		return ClaimConflict, fmt.Errorf("lookup queue event %s: %w", msg.EventID, err)
	}

	var request struct {
		eventID      string
		fingerprint  string
		state        string
		leaseExpires nullableClaimTime
	}
	err = tx.QueryRowContext(ctx, `
		SELECT event_id, fingerprint, state, lease_expires_at
		FROM zatca_consumer_claim
		WHERE tenant_id = ? AND pos_id = ? AND bill_request_id = ?
		FOR UPDATE
	`, msg.TenantID, msg.PosID, msg.BillRequestID).Scan(
		&request.eventID,
		&request.fingerprint,
		&request.state,
		&request.leaseExpires,
	)
	switch {
	case err == nil:
		if request.fingerprint != fingerprint {
			if err := tx.Commit(); err != nil {
				return ClaimConflict, fmt.Errorf("commit queue request conflict: %w", err)
			}
			return ClaimConflict, nil
		}
		decision := sqlClaimDecision(request.state, request.leaseExpires, now)
		if decision == ClaimProcess {
			if _, err := tx.ExecContext(ctx, `
				UPDATE zatca_consumer_claim
				SET event_id = ?, egs_sequence_id = ?, state = 'processing',
				    lease_expires_at = ?, updated_at = CURRENT_TIMESTAMP
				WHERE tenant_id = ? AND pos_id = ? AND bill_request_id = ?
			`, msg.EventID, msg.EGSSequenceID, leaseExpires,
				msg.TenantID, msg.PosID, msg.BillRequestID); err != nil {
				return ClaimConflict, fmt.Errorf("reclaim queue request %s: %w", msg.BillRequestID, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return ClaimConflict, fmt.Errorf("commit queue request %s: %w", msg.BillRequestID, err)
		}
		return decision, nil
	case !errors.Is(err, sql.ErrNoRows):
		return ClaimConflict, fmt.Errorf("lookup queue request %s: %w", msg.BillRequestID, err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO zatca_consumer_claim
			(tenant_id, pos_id, bill_request_id, event_id, egs_sequence_id,
			 fingerprint, state, lease_expires_at)
		VALUES (?, ?, ?, ?, ?, ?, 'processing', ?)
	`, msg.TenantID, msg.PosID, msg.BillRequestID, msg.EventID,
		msg.EGSSequenceID, fingerprint, leaseExpires); err != nil {
		return ClaimConflict, fmt.Errorf("insert queue claim %s: %w", msg.BillRequestID, err)
	}
	if err := tx.Commit(); err != nil {
		return ClaimConflict, fmt.Errorf("commit new queue claim %s: %w", msg.BillRequestID, err)
	}
	return ClaimProcess, nil
}

func sqlClaimDecision(state string, lease nullableClaimTime, now time.Time) ClaimDecision {
	if state == "completed" {
		return ClaimDuplicate
	}
	if lease.Valid && lease.Time.After(now) {
		return ClaimInFlight
	}
	return ClaimProcess
}

// Complete records the durable business effect and is safe to repeat.
func (s *SQLClaimStore) Complete(ctx context.Context, msg Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	if s == nil || s.DB == nil {
		return errors.New("queue SQL claim store is not configured")
	}

	result, err := s.DB.ExecContext(ctx, `
		UPDATE zatca_consumer_claim
		SET state = 'completed', lease_expires_at = NULL,
		    completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE tenant_id = ? AND pos_id = ? AND bill_request_id = ?
		  AND fingerprint = ? AND state = 'processing'
	`, msg.TenantID, msg.PosID, msg.BillRequestID, msg.BusinessFingerprint())
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
		WHERE tenant_id = ? AND pos_id = ? AND bill_request_id = ?
		  AND fingerprint = ?
	`, msg.TenantID, msg.PosID, msg.BillRequestID, msg.BusinessFingerprint()).Scan(&state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: %s", ErrClaimNotFound, msg.RequestKey())
	case err != nil:
		return fmt.Errorf("inspect queue claim %s: %w", msg.BillRequestID, err)
	case state == "completed":
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrClaimInFlight, msg.RequestKey())
	}
}
