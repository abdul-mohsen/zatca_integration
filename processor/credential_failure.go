package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/credential"
	"github.com/zatca-go/zatca/zatca"
)

var ErrCredentialFailureRecorderRequired = errors.New("credential failure recorder is required")

// CredentialFailureRecorder persists an operator-actionable credential
// failure. Implementations must not acknowledge the queue message.
type CredentialFailureRecorder interface {
	RecordCredentialFailure(context.Context, credential.Identity, error) error
}

// SQLCredentialFailureRecorder records the failure in the existing
// branch_zatca_config state machine so the backend/operator can repair the
// branch without introducing a second credential store.
type SQLCredentialFailureRecorder struct {
	DB  *sql.DB
	Now func() time.Time
}

func (r *SQLCredentialFailureRecorder) RecordCredentialFailure(
	ctx context.Context,
	identity credential.Identity,
	cause error,
) error {
	if r == nil || r.DB == nil {
		return ErrCredentialFailureRecorderRequired
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	message := "credential preflight failed"
	if cause != nil {
		message = cause.Error()
	}
	result, err := r.DB.ExecContext(ctx, `
		UPDATE branch_zatca_config
		SET onboard_state = ?,
		    last_error = ?,
		    last_attempt_at = ?
		WHERE branch_id = ?
	`, OnboardingStateCredentialFailure, message, r.now(), identity.BranchID)
	if err != nil {
		return fmt.Errorf("persist credential failure for branch %d: %w", identity.BranchID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect credential failure for branch %d: %w", identity.BranchID, err)
	}
	if affected != 1 {
		return fmt.Errorf("persist credential failure for branch %d: row not found", identity.BranchID)
	}
	return nil
}

func (r *SQLCredentialFailureRecorder) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// CredentialPreflightError preserves the failed gate reason and whether the
// action-required state was durably recorded.
type CredentialPreflightError struct {
	Identity  credential.Identity
	Cause     error
	Persisted bool
}

func (e *CredentialPreflightError) Error() string {
	if e == nil {
		return "credential preflight failed"
	}
	if e.Cause == nil {
		return fmt.Sprintf("credential preflight failed for branch %d", e.Identity.BranchID)
	}
	return fmt.Sprintf("credential preflight failed for branch %d: %v", e.Identity.BranchID, e.Cause)
}

func (e *CredentialPreflightError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// RunCredentialPreflight validates credentials and persists failure state
// before any caller may reserve an ICV or invoke the provider.
func RunCredentialPreflight(
	ctx context.Context,
	gate *credential.PreflightGate,
	input credential.PreflightInput,
	recorder CredentialFailureRecorder,
) error {
	if gate == nil {
		cause := credential.ErrPreflightGateUnavailable
		return persistCredentialPreflightFailure(ctx, input.Identity, cause, recorder)
	}
	if err := gate.Check(input); err != nil {
		return persistCredentialPreflightFailure(ctx, input.Identity, err, recorder)
	}
	return nil
}

// RecordCredentialFailure persists a preflight failure that occurred while
// loading the canonical state or runtime material before the gate ran.
func RecordCredentialFailure(
	ctx context.Context,
	identity credential.Identity,
	cause error,
	recorder CredentialFailureRecorder,
) error {
	return persistCredentialPreflightFailure(ctx, identity, cause, recorder)
}

func persistCredentialPreflightFailure(
	ctx context.Context,
	identity credential.Identity,
	cause error,
	recorder CredentialFailureRecorder,
) error {
	if recorder == nil {
		return &CredentialPreflightError{Identity: identity, Cause: errors.Join(cause, ErrCredentialFailureRecorderRequired)}
	}
	if err := recorder.RecordCredentialFailure(ctx, identity, cause); err != nil {
		return &CredentialPreflightError{
			Identity: identity,
			Cause:    errors.Join(cause, fmt.Errorf("persist credential failure: %w", err)),
		}
	}
	return &CredentialPreflightError{Identity: identity, Cause: cause, Persisted: true}
}

const sharedSDKRoot = "/SDK/zatca-einvoicing-sdk-238-R4.0.0"

// CheckSDKReadiness verifies only local shared-SDK availability. It does not
// execute the SDK, write its read-only package, or contact ZATCA.
func CheckSDKReadiness(svc *zatca.Service) error {
	if svc == nil || svc.SDK == nil {
		return errors.New("SDK service is not configured")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		return fmt.Errorf("bash runtime is unavailable: %w", err)
	}
	for _, path := range []string{
		sharedSDKRoot,
		filepath.Join(sharedSDKRoot, "Apps"),
		filepath.Join(sharedSDKRoot, "Apps", "fatoora"),
		filepath.Join(sharedSDKRoot, "Data", "Certificates"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("shared SDK path %s: %w", path, err)
		}
		if path != filepath.Join(sharedSDKRoot, "Apps", "fatoora") && !info.IsDir() {
			return fmt.Errorf("shared SDK path %s is not a directory", path)
		}
		if path == filepath.Join(sharedSDKRoot, "Apps", "fatoora") && info.IsDir() {
			return fmt.Errorf("shared SDK executable path %s is a directory", path)
		}
	}
	return nil
}

// EGSWorkingPaths returns the root and per-EGS directory that must be
// available before a document can be reserved or written.
func EGSWorkingPaths(cfg *config.Config, branchID int64, egsSequenceID string) ([]string, error) {
	if cfg == nil {
		return nil, errors.New("ZATCA configuration is required")
	}
	if strings.TrimSpace(cfg.XMLDir) == "" {
		return nil, errors.New("XML working directory is required")
	}
	if branchID <= 0 {
		return nil, errors.New("branch ID is required for EGS working path")
	}
	tenantSegment, err := safePathSegment(cfg.TenantID, "tenant")
	if err != nil {
		return nil, err
	}
	egsSegment, err := safePathSegment(egsSequenceID, "EGS sequence")
	if err != nil {
		return nil, err
	}
	egspath := filepath.Join(cfg.XMLDir, tenantSegment, fmt.Sprintf("branch-%d", branchID), egsSegment)
	return []string{cfg.XMLDir, egspath}, nil
}

// EnsureEGSWorkingPaths creates only the per-EGS directory after verifying
// that the configured root exists. The shared SDK package is never modified.
func EnsureEGSWorkingPaths(paths []string) error {
	if len(paths) < 2 {
		return errors.New("root and per-EGS working paths are required")
	}
	rootInfo, err := os.Stat(paths[0])
	if err != nil {
		return fmt.Errorf("inspect XML working directory: %w", err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("XML working path %s is not a directory", paths[0])
	}
	if err := os.MkdirAll(paths[1], 0750); err != nil {
		return fmt.Errorf("create EGS working path %s: %w", paths[1], err)
	}
	return nil
}

func safePathSegment(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." ||
		strings.ContainsAny(value, `/\:`) || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s path segment is invalid", label)
	}
	return value, nil
}
