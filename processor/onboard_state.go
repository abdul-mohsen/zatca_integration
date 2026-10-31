package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type OnboardingState string

const (
	OnboardingStateNotStarted OnboardingState = "not_started"
	OnboardingStateCSR        OnboardingState = "csr"
	OnboardingStateCompliance OnboardingState = "compliance"
	OnboardingStateInvoices   OnboardingState = "invoices"
	OnboardingStateProduction OnboardingState = "production"
	OnboardingStateDone       OnboardingState = "done"
	OnboardingStateFailed     OnboardingState = "failed"
)

type OnboardingStateStore interface {
	SetOnboardingState(context.Context, int64, OnboardingState, string) error
}

// SQLOnboardingStateStore persists the onboarding state machine in the
// branch_zatca_config row. It deliberately stores no OTP.
type SQLOnboardingStateStore struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s *SQLOnboardingStateStore) SetOnboardingState(ctx context.Context, branchID int64, state OnboardingState, lastError string) error {
	if s == nil || s.DB == nil {
		return errors.New("onboarding state database is not configured")
	}
	if branchID <= 0 {
		return errors.New("onboarding branch_id is required")
	}
	if !validOnboardingState(state) {
		return fmt.Errorf("invalid onboarding state %q", state)
	}
	var errorValue any
	if strings.TrimSpace(lastError) != "" {
		errorValue = lastError
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE branch_zatca_config
		SET onboard_state = ?,
		    last_error = ?,
		    last_attempt_at = ?
		WHERE branch_id = ?
	`, state, errorValue, s.now(), branchID)
	if err != nil {
		return fmt.Errorf("persist onboarding state %s for branch %d: %w", state, branchID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect onboarding state for branch %d: %w", branchID, err)
	}
	if affected != 1 {
		return fmt.Errorf("persist onboarding state for branch %d: row not found", branchID)
	}
	return nil
}

func (s *SQLOnboardingStateStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func validOnboardingState(state OnboardingState) bool {
	switch state {
	case OnboardingStateNotStarted,
		OnboardingStateCSR,
		OnboardingStateCompliance,
		OnboardingStateInvoices,
		OnboardingStateProduction,
		OnboardingStateDone,
		OnboardingStateFailed:
		return true
	default:
		return false
	}
}

// RunTrackedOnboarding provides one durable state transition seam for the
// provider-backed onboarding flow. The operation reports provider stages via
// progress; any provider or persistence error moves the row to failed.
func RunTrackedOnboarding(
	ctx context.Context,
	branchID int64,
	store OnboardingStateStore,
	operation func(func(OnboardingState) error) error,
) (err error) {
	if store == nil {
		return errors.New("onboarding state store is required")
	}
	if operation == nil {
		return errors.New("onboarding operation is required")
	}
	if err := store.SetOnboardingState(ctx, branchID, OnboardingStateCSR, ""); err != nil {
		return err
	}

	progress := func(state OnboardingState) error {
		if !validOnboardingState(state) || state == OnboardingStateDone || state == OnboardingStateFailed {
			return fmt.Errorf("invalid provider onboarding stage %q", state)
		}
		return store.SetOnboardingState(ctx, branchID, state, "")
	}

	defer func() {
		if err == nil {
			if doneErr := store.SetOnboardingState(ctx, branchID, OnboardingStateDone, ""); doneErr != nil {
				err = doneErr
			}
			return
		}
		failedErr := store.SetOnboardingState(ctx, branchID, OnboardingStateFailed, err.Error())
		if failedErr != nil {
			err = errors.Join(err, failedErr)
		}
	}()

	err = operation(progress)
	return err
}
