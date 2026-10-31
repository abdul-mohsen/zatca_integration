package credential

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SQLStore implements the backend CSID lifecycle contract directly against a
// tenant database. The canonical row is metadata-only; secret material is
// handled by a separate MaterialStore.
type SQLStore struct {
	DB *sql.DB
}

func (s *SQLStore) Load(ctx context.Context, identity Identity) (State, error) {
	if s == nil || s.DB == nil {
		return State{}, ErrLifecycleStoreRequired
	}
	if err := identity.Validate(); err != nil {
		return State{}, err
	}

	var (
		state                 State
		serial                sql.NullString
		expiresAt             nullableTime
		status                string
		renewalState          string
		revocationState       string
		renewalRequestedAt    nullableTime
		renewedAt             nullableTime
		revocationRequestedAt nullableTime
		revokedAt             nullableTime
		reason                sql.NullString
		lastError             sql.NullString
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT
			certificate_serial,
			certificate_expires_at,
			status,
			renewal_state,
			revocation_state,
			renewal_requested_at,
			renewed_at,
			revocation_requested_at,
			revoked_at,
			revocation_reason,
			last_error
		FROM zatca_egs_credential
		WHERE tenant_id = ?
		  AND branch_id = ?
		  AND egs_sequence_id = ?
		  AND credential_type = ?
		LIMIT 1
	`, identity.TenantID, identity.BranchID, identity.EGSSequenceID, identity.CredentialType).Scan(
		&serial,
		&expiresAt,
		&status,
		&renewalState,
		&revocationState,
		&renewalRequestedAt,
		&renewedAt,
		&revocationRequestedAt,
		&revokedAt,
		&reason,
		&lastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, fmt.Errorf("%w: tenant=%d branch=%d egs=%s type=%s",
			ErrCredentialNotFound, identity.TenantID, identity.BranchID,
			identity.EGSSequenceID, identity.CredentialType)
	}
	if err != nil {
		return State{}, fmt.Errorf("load canonical credential: %w", err)
	}

	state.Identity = identity
	state.CertificateSerial = serial.String
	state.Status = Status(status)
	state.RenewalState = RenewalState(renewalState)
	state.RevocationState = RevocationState(revocationState)
	state.RevocationReason = reason.String
	state.LastError = lastError.String
	state.CertificateExpiresAt = nullTimePointer(expiresAt)
	state.RenewalRequestedAt = nullTimePointer(renewalRequestedAt)
	state.RenewedAt = nullTimePointer(renewedAt)
	state.RevocationRequestedAt = nullTimePointer(revocationRequestedAt)
	state.RevokedAt = nullTimePointer(revokedAt)
	return state, nil
}

func (s *SQLStore) MarkExpired(ctx context.Context, identity Identity, asOf time.Time) error {
	if s == nil || s.DB == nil {
		return ErrLifecycleStoreRequired
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE zatca_egs_credential
		SET status = 'expired',
		    last_error = NULL
		WHERE tenant_id = ?
		  AND branch_id = ?
		  AND egs_sequence_id = ?
		  AND credential_type = ?
		  AND status = 'active'
		  AND revocation_state = 'none'
		  AND certificate_expires_at IS NOT NULL
		  AND certificate_expires_at <= ?
	`, identity.TenantID, identity.BranchID, identity.EGSSequenceID, identity.CredentialType, asOf)
	return requireOneLifecycleUpdate(result, err, "mark credential expired")
}

func (s *SQLStore) MarkRenewalFailed(ctx context.Context, identity Identity, lastError string) error {
	if s == nil || s.DB == nil {
		return ErrLifecycleStoreRequired
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE zatca_egs_credential
		SET renewal_state = 'failed',
		    last_error = ?
		WHERE tenant_id = ?
		  AND branch_id = ?
		  AND egs_sequence_id = ?
		  AND credential_type = ?
		  AND status = 'pending'
		  AND revocation_state = 'none'
	`, lastError, identity.TenantID, identity.BranchID, identity.EGSSequenceID, identity.CredentialType)
	return requireOneLifecycleUpdate(result, err, "mark credential renewal failed")
}

func (s *SQLStore) RecordIssued(ctx context.Context, identity Identity, issued IssuedCredential, issuedAt time.Time) error {
	if s == nil || s.DB == nil {
		return ErrLifecycleStoreRequired
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE zatca_egs_credential
		SET certificate_serial = ?,
		    certificate_expires_at = ?,
		    status = 'active',
		    renewal_state = 'completed',
		    renewed_at = ?,
		    revocation_state = 'none',
		    revocation_requested_at = NULL,
		    last_error = NULL
		WHERE tenant_id = ?
		  AND branch_id = ?
		  AND egs_sequence_id = ?
		  AND credential_type = ?
		  AND status IN ('pending','active','expired')
		  AND revocation_state = 'none'
	`, issued.CertificateSerial, issued.CertificateExpiresAt, issuedAt,
		identity.TenantID, identity.BranchID, identity.EGSSequenceID, identity.CredentialType)
	return requireOneLifecycleUpdate(result, err, "record issued credential")
}

func (s *SQLStore) MarkRevocationFailed(ctx context.Context, identity Identity, lastError string) error {
	if s == nil || s.DB == nil {
		return ErrLifecycleStoreRequired
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE zatca_egs_credential
		SET last_error = ?
		WHERE tenant_id = ?
		  AND branch_id = ?
		  AND egs_sequence_id = ?
		  AND credential_type = ?
		  AND revocation_state = 'pending'
		  AND status <> 'revoked'
	`, lastError, identity.TenantID, identity.BranchID, identity.EGSSequenceID, identity.CredentialType)
	return requireOneLifecycleUpdate(result, err, "mark credential revocation failure")
}

func (s *SQLStore) MarkRevoked(ctx context.Context, identity Identity, reason string, revokedAt time.Time) error {
	if s == nil || s.DB == nil {
		return ErrLifecycleStoreRequired
	}
	var reasonValue any
	if reason != "" {
		reasonValue = reason
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE zatca_egs_credential
		SET status = 'revoked',
		    revocation_state = 'revoked',
		    revoked_at = ?,
		    revocation_reason = ?,
		    last_error = NULL
		WHERE tenant_id = ?
		  AND branch_id = ?
		  AND egs_sequence_id = ?
		  AND credential_type = ?
		  AND status <> 'revoked'
		  AND revocation_state IN ('none','pending')
	`, revokedAt, reasonValue, identity.TenantID, identity.BranchID, identity.EGSSequenceID, identity.CredentialType)
	return requireOneLifecycleUpdate(result, err, "mark credential revoked")
}

func requireOneLifecycleUpdate(result sql.Result, err error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", operation, err)
	}
	if affected != 1 {
		return fmt.Errorf("%s: lifecycle transition was not applied", operation)
	}
	return nil
}

type nullableTime struct {
	time.Time
	Valid bool
}

func (value *nullableTime) Scan(src any) error {
	switch typed := src.(type) {
	case nil:
		value.Time = time.Time{}
		value.Valid = false
		return nil
	case time.Time:
		value.Time = typed
		value.Valid = true
		return nil
	case []byte:
		return value.Scan(string(typed))
	case string:
		for _, layout := range []string{
			"2006-01-02 15:04:05",
			time.RFC3339Nano,
			time.RFC3339,
		} {
			parsed, err := time.ParseInLocation(layout, typed, time.UTC)
			if err == nil {
				value.Time = parsed
				value.Valid = true
				return nil
			}
		}
		return fmt.Errorf("unsupported datetime %q", typed)
	default:
		return fmt.Errorf("unsupported datetime type %T", src)
	}
}

func nullTimePointer(value nullableTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}
