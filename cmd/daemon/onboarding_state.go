package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/zatca-go/zatca/client"
	"github.com/zatca-go/zatca/processor"
	"github.com/zatca-go/zatca/queue"
)

const maxOnboardingErrorLength = 1024

type onboardingProgress struct {
	BranchID      int64
	AttemptNumber uint64
	State         string
	Step          string
	RetryCount    uint32
	ErrorCode     *string
	ErrorMessage  *string
}

type onboardingStateStore interface {
	Queue(branchID int64) (uint64, error)
	Progress(progress onboardingProgress) error
	Fail(branchID int64, attemptNumber uint64, errorCode, errorMessage string) error
	Complete(branchID int64, attemptNumber uint64) error
}

type onboardingRunFunc func(*tenantCtx, queue.Message, func(string) error) error

type sqlOnboardingStateStore struct {
	db *sql.DB
}

func onboardingStateStoreFor(tc *tenantCtx) onboardingStateStore {
	if tc == nil {
		return nil
	}
	if tc.onboardingState != nil {
		return tc.onboardingState
	}
	db := tc.adminDB
	if db == nil {
		db = tc.db
	}
	return &sqlOnboardingStateStore{db: db}
}

func (s *sqlOnboardingStateStore) Queue(branchID int64) (uint64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("onboarding state database is unavailable")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin onboarding queue state: %w", err)
	}
	defer tx.Rollback()

	var state string
	var attemptNumber uint64
	if err := tx.QueryRow(
		`SELECT COALESCE(onboard_state, 'not_started'), COALESCE(onboard_attempt, 0)
		 FROM branch_zatca_config
		 WHERE branch_id = ?
		 FOR UPDATE`,
		branchID,
	).Scan(&state, &attemptNumber); err != nil {
		return 0, fmt.Errorf("load onboarding state for branch %d: %w", branchID, err)
	}

	if state == "done" {
		return 0, fmt.Errorf("branch %d onboarding is already complete", branchID)
	}
	if state != "queued" && state != "failed" && state != "not_started" {
		return 0, fmt.Errorf("branch %d onboarding is already active at %s", branchID, state)
	}

	if attemptNumber == 0 {
		attemptNumber = 1
	}
	if _, err := tx.Exec(
		`UPDATE branch_zatca_config
		 SET onboard_state = 'queued',
		     onboard_step = 'queued',
		     onboard_attempt = ?,
		     onboard_retry_count = 0,
		     onboard_error_code = NULL,
		     onboard_error = NULL,
		     last_error = NULL,
		     onboard_queued_at = CURRENT_TIMESTAMP,
		     onboard_started_at = NULL,
		     onboard_completed_at = NULL,
		     last_attempt_at = CURRENT_TIMESTAMP
		 WHERE branch_id = ?`,
		attemptNumber,
		branchID,
	); err != nil {
		return 0, fmt.Errorf("queue onboarding branch %d: %w", branchID, err)
	}

	if _, err := tx.Exec(
		`INSERT INTO zatca_onboarding_attempt
		   (branch_id, attempt_number, state, step, retry_count, queued_at)
		 VALUES (?, ?, 'queued', 'queued', 0, CURRENT_TIMESTAMP)
		 ON DUPLICATE KEY UPDATE
		   state = 'queued',
		   step = 'queued',
		   retry_count = 0,
		   error_code = NULL,
		   error_message = NULL,
		   queued_at = CURRENT_TIMESTAMP,
		   started_at = NULL,
		   completed_at = NULL`,
		branchID,
		attemptNumber,
	); err != nil {
		return 0, fmt.Errorf("queue onboarding attempt for branch %d: %w", branchID, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit onboarding queue state for branch %d: %w", branchID, err)
	}
	return attemptNumber, nil
}

func (s *sqlOnboardingStateStore) Progress(progress onboardingProgress) error {
	if s == nil || s.db == nil {
		return errors.New("onboarding state database is unavailable")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin onboarding progress: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`UPDATE branch_zatca_config
		 SET onboard_state = ?,
		     onboard_step = ?,
		     onboard_retry_count = ?,
		     onboard_error_code = ?,
		     onboard_error = ?,
		     last_error = ?,
		     onboard_started_at = COALESCE(onboard_started_at, CURRENT_TIMESTAMP),
		     last_attempt_at = CURRENT_TIMESTAMP
		 WHERE branch_id = ?
		   AND onboard_attempt = ?`,
		progress.State,
		progress.Step,
		progress.RetryCount,
		progress.ErrorCode,
		progress.ErrorMessage,
		progress.ErrorMessage,
		progress.BranchID,
		progress.AttemptNumber,
	)
	if err != nil {
		return fmt.Errorf("update onboarding branch progress: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("inspect onboarding branch progress: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("onboarding attempt %d for branch %d is no longer current",
			progress.AttemptNumber, progress.BranchID)
	}

	attemptResult, err := tx.Exec(
		`UPDATE zatca_onboarding_attempt
		 SET state = ?,
		     step = ?,
		     retry_count = ?,
		     error_code = ?,
		     error_message = ?,
		     started_at = COALESCE(started_at, CURRENT_TIMESTAMP)
		 WHERE branch_id = ?
		   AND attempt_number = ?`,
		progress.State,
		progress.Step,
		progress.RetryCount,
		progress.ErrorCode,
		progress.ErrorMessage,
		progress.BranchID,
		progress.AttemptNumber,
	)
	if err != nil {
		return fmt.Errorf("update onboarding attempt progress: %w", err)
	}
	if affected, err := attemptResult.RowsAffected(); err != nil {
		return fmt.Errorf("inspect onboarding attempt progress: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("onboarding attempt history %d for branch %d is missing",
			progress.AttemptNumber, progress.BranchID)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit onboarding progress: %w", err)
	}
	return nil
}

func (s *sqlOnboardingStateStore) Fail(branchID int64, attemptNumber uint64, errorCode, errorMessage string) error {
	if s == nil || s.db == nil {
		return errors.New("onboarding state database is unavailable")
	}
	errorCode = boundOnboardingError(errorCode)
	errorMessage = boundOnboardingError(errorMessage)

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin onboarding failure: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`UPDATE branch_zatca_config
		 SET onboard_state = 'failed',
		     onboard_step = 'failed',
		     onboard_retry_count = onboard_retry_count + 1,
		     onboard_error_code = ?,
		     onboard_error = ?,
		     last_error = ?,
		     onboard_started_at = COALESCE(onboard_started_at, CURRENT_TIMESTAMP),
		     last_attempt_at = CURRENT_TIMESTAMP
		 WHERE branch_id = ?
		   AND onboard_attempt = ?`,
		errorCode,
		errorMessage,
		errorMessage,
		branchID,
		attemptNumber,
	)
	if err != nil {
		return fmt.Errorf("update failed onboarding branch state: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("inspect failed onboarding branch state: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("onboarding attempt %d for branch %d is no longer current",
			attemptNumber, branchID)
	}

	attemptResult, err := tx.Exec(
		`UPDATE zatca_onboarding_attempt
		 SET state = 'failed',
		     step = 'failed',
		     retry_count = retry_count + 1,
		     error_code = ?,
		     error_message = ?,
		     started_at = COALESCE(started_at, CURRENT_TIMESTAMP),
		     completed_at = CURRENT_TIMESTAMP
		 WHERE branch_id = ?
		   AND attempt_number = ?`,
		errorCode,
		errorMessage,
		branchID,
		attemptNumber,
	)
	if err != nil {
		return fmt.Errorf("update failed onboarding attempt: %w", err)
	}
	if affected, err := attemptResult.RowsAffected(); err != nil {
		return fmt.Errorf("inspect failed onboarding attempt: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("onboarding attempt history %d for branch %d is missing",
			attemptNumber, branchID)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failed onboarding state: %w", err)
	}
	return nil
}

func (s *sqlOnboardingStateStore) Complete(branchID int64, attemptNumber uint64) error {
	if s == nil || s.db == nil {
		return errors.New("onboarding state database is unavailable")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin onboarding completion: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`UPDATE branch_zatca_config
		 SET onboard_state = 'done',
		     onboard_step = 'done',
		     onboard_error_code = NULL,
		     onboard_error = NULL,
		     last_error = NULL,
		     onboard_started_at = COALESCE(onboard_started_at, CURRENT_TIMESTAMP),
		     onboard_completed_at = CURRENT_TIMESTAMP,
		     zatca_onboarded_at = COALESCE(zatca_onboarded_at, CURRENT_TIMESTAMP),
		     last_attempt_at = CURRENT_TIMESTAMP
		 WHERE branch_id = ?
		   AND onboard_attempt = ?`,
		branchID,
		attemptNumber,
	)
	if err != nil {
		return fmt.Errorf("update completed onboarding branch state: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("inspect completed onboarding branch state: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("onboarding attempt %d for branch %d is no longer current",
			attemptNumber, branchID)
	}

	attemptResult, err := tx.Exec(
		`UPDATE zatca_onboarding_attempt
		 SET state = 'done',
		     step = 'done',
		     started_at = COALESCE(started_at, CURRENT_TIMESTAMP),
		     completed_at = CURRENT_TIMESTAMP
		 WHERE branch_id = ?
		   AND attempt_number = ?`,
		branchID,
		attemptNumber,
	)
	if err != nil {
		return fmt.Errorf("update completed onboarding attempt: %w", err)
	}
	if affected, err := attemptResult.RowsAffected(); err != nil {
		return fmt.Errorf("inspect completed onboarding attempt: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("onboarding attempt history %d for branch %d is missing",
			attemptNumber, branchID)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit completed onboarding state: %w", err)
	}
	return nil
}

func handleOnboardMessage(
	tc *tenantCtx,
	msg queue.Message,
	store onboardingStateStore,
	run onboardingRunFunc,
	onSuccess func(queue.Message),
	ack func() error,
) {
	if store == nil {
		logOnboardingStateError(msg, errors.New("onboarding state store is unavailable"))
		return
	}
	attemptNumber, err := store.Queue(msg.BranchID)
	if err != nil {
		logOnboardingStateError(msg, err)
		return
	}

	report := func(step string) error {
		state, err := onboardingStateForStep(step)
		if err != nil {
			return err
		}
		return store.Progress(onboardingProgress{
			BranchID:      msg.BranchID,
			AttemptNumber: attemptNumber,
			State:         state,
			Step:          step,
			RetryCount:    0,
		})
	}

	if run == nil {
		run = runOnboarding
	}
	err = run(tc, msg, report)
	if err != nil {
		errorCode, errorMessage := normalizeOnboardingError(err, msg.OTP)
		if stateErr := store.Fail(msg.BranchID, attemptNumber, errorCode, errorMessage); stateErr != nil {
			logOnboardingStateError(msg, fmt.Errorf("persist failure after %s: %w", errorCode, stateErr))
			return
		}
		if tc != nil && tc.tenant.AlertEmail != "" {
			sendAlert(tc.tenant.AlertEmail, msg, err)
		}
		if ack != nil {
			if ackErr := ack(); ackErr != nil {
				logOnboardingStateError(msg, fmt.Errorf("ack failed after durable failure: %w", ackErr))
			}
		}
		return
	}

	if err := store.Complete(msg.BranchID, attemptNumber); err != nil {
		logOnboardingStateError(msg, err)
		return
	}
	if onSuccess != nil {
		onSuccess(msg)
	}
	if ack != nil {
		if err := ack(); err != nil {
			logOnboardingStateError(msg, fmt.Errorf("ack failed after durable success: %w", err))
		}
	}
}

func runOnboarding(tc *tenantCtx, msg queue.Message, progress func(string) error) error {
	if tc == nil {
		return errors.New("onboarding tenant context is unavailable")
	}
	db := tc.adminDB
	if db == nil {
		db = tc.db
	}
	return processor.OnboardBranchWithProgress(db, tc.cfg, msg.BranchID, msg.OTP, tc.encKey, progress)
}

func onboardingStateForStep(step string) (string, error) {
	switch step {
	case "csr":
		return "queued", nil
	case "compliance":
		return "compliance", nil
	case "invoices":
		return "compliance", nil
	case "production":
		return "production", nil
	default:
		return "", fmt.Errorf("unknown onboarding step %q", step)
	}
}

func normalizeOnboardingError(err error, otp string) (string, string) {
	if err == nil {
		return "onboarding_error", "onboarding failed"
	}
	message := strings.TrimSpace(err.Error())
	if otp != "" {
		message = strings.ReplaceAll(message, otp, "[redacted]")
	}
	message = boundOnboardingError(message)
	if message == "" {
		message = "onboarding failed"
	}
	lower := strings.ToLower(message)

	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 401 || apiErr.StatusCode == 403 ||
			strings.Contains(strings.ToLower(apiErr.Body), "invalid otp") {
			return "invalid_otp", message
		}
		if apiErr.StatusCode >= 500 {
			return "provider_unavailable", message
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) || strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "provider unavailable") ||
		strings.Contains(lower, "temporary provider outage") ||
		strings.Contains(lower, "http request failed") {
		return "provider_unavailable", message
	}
	if strings.Contains(lower, "invalid otp") ||
		(strings.Contains(lower, "otp") &&
			(strings.Contains(lower, "invalid") ||
				strings.Contains(lower, "unauthorized") ||
				strings.Contains(lower, "forbidden") ||
				strings.Contains(lower, "expired") ||
				strings.Contains(lower, "rejected"))) {
		return "invalid_otp", message
	}
	return "onboarding_error", message
}

func boundOnboardingError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > maxOnboardingErrorLength {
		return message[:maxOnboardingErrorLength]
	}
	return message
}

func logOnboardingStateError(msg queue.Message, err error) {
	log.Printf("ERROR: onboarding state branch=%d db=%s: %v", msg.BranchID, msg.DBName, err)
}
