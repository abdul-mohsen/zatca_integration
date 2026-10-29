package processor

import (
	"errors"
	"testing"
	"time"

	"github.com/zatca-go/zatca/models"
)

type idempotencyProvider struct {
	submitCalls     int
	reconcileCalls  int
	submittedUUIDs  []string
	submitResult    *ProviderResult
	submitErr       error
	reconcileResult *ProviderResult
	reconcileErr    error
}

func (p *idempotencyProvider) SignInvoice(string) (string, string, error) {
	return "<signed-invoice/>", "hash-41", nil
}

func (p *idempotencyProvider) GenerateAPIRequest(string) (models.InvoiceRequest, error) {
	return models.InvoiceRequest{
		InvoiceHash: "hash-41",
		UUID:        "00000000-0000-0000-0000-000000000041",
		Invoice:     "c2lnbmVkLWludm9pY2U=",
	}, nil
}

func (p *idempotencyProvider) GenerateQR(string) (string, error) {
	return "qr-41", nil
}

func (p *idempotencyProvider) Submit(request models.InvoiceRequest, _ bool) (*ProviderResult, error) {
	p.submitCalls++
	p.submittedUUIDs = append(p.submittedUUIDs, request.UUID)
	return p.submitResult, p.submitErr
}

func (p *idempotencyProvider) Reconcile(ProviderIdentity, bool) (*ProviderResult, error) {
	p.reconcileCalls++
	return p.reconcileResult, p.reconcileErr
}

type idempotencyStore struct {
	state       OutcomeState
	starts      int
	completions int
	attempt     submissionAttempt
	outcome     submissionOutcome
	update      documentUpdate
}

func (s *idempotencyStore) StartSubmission(_ submissionMeta, documentID string, startedAt time.Time) (submissionAttempt, error) {
	if s.state == OutcomeAccepted || s.state == OutcomeAcceptedWithWarning {
		return submissionAttempt{
			submissionID: 10,
			merchantID:   7,
			documentID:   documentID,
			resolved:     true,
			invoiceHash:  "hash-41",
		}, nil
	}
	if s.state == OutcomeAmbiguous {
		s.attempt.needsReconciliation = true
		return s.attempt, nil
	}
	s.starts++
	s.attempt = submissionAttempt{
		submissionID:  10,
		attemptID:     int64(20 + s.starts),
		attemptNumber: s.starts,
		merchantID:    7,
		documentID:    documentID,
		startedAt:     startedAt,
	}
	return s.attempt, nil
}

func (s *idempotencyStore) StartRetrySubmission(meta submissionMeta, documentID string, startedAt time.Time) (submissionAttempt, error) {
	s.state = OutcomePending
	return s.StartSubmission(meta, documentID, startedAt)
}

func (s *idempotencyStore) CompleteSubmission(
	attempt submissionAttempt,
	outcome submissionOutcome,
	update documentUpdate,
	_ []artifactRecord,
) error {
	s.completions++
	s.state = outcome.State
	s.attempt = attempt
	s.outcome = outcome
	s.update = update
	return nil
}

func TestDuplicateDeliveryShortCircuitsAfterAcceptance(t *testing.T) {
	provider := &idempotencyProvider{
		submitResult: &ProviderResult{State: OutcomeAccepted, ProviderCode: "REPORTED"},
	}
	store := &idempotencyStore{}
	inv := outcomeTestInvoice()
	meta := outcomeTestMeta()
	meta.stableDocumentKey = "tenant-a:2:bill:41"

	if _, err := signSubmitUpdateWithProvider(provider, store, inv, 41, meta); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if _, err := signSubmitUpdateWithProvider(provider, store, inv, 41, meta); err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}
	if provider.submitCalls != 1 {
		t.Fatalf("submit calls = %d, want 1", provider.submitCalls)
	}
	if provider.reconcileCalls != 0 {
		t.Fatalf("reconcile calls = %d, want 0", provider.reconcileCalls)
	}
}

func TestTimeoutAfterAcceptanceReconcilesBeforeResubmitting(t *testing.T) {
	provider := &idempotencyProvider{
		submitErr: &ProviderError{
			State:   OutcomeAmbiguous,
			Code:    "TIMEOUT",
			Message: "provider accepted then timed out",
		},
		reconcileResult: &ProviderResult{
			State:        OutcomeAcceptedWithWarning,
			ProviderCode: "REPORTED",
			WarningCode:  "W-01",
		},
	}
	store := &idempotencyStore{}
	inv := outcomeTestInvoice()
	meta := outcomeTestMeta()
	meta.stableDocumentKey = "tenant-a:2:bill:41"

	if _, err := signSubmitUpdateWithProvider(provider, store, inv, 41, meta); err == nil {
		t.Fatal("first delivery unexpectedly succeeded")
	}
	if _, err := signSubmitUpdateWithProvider(provider, store, inv, 41, meta); err != nil {
		t.Fatalf("reconciled delivery: %v", err)
	}
	if provider.submitCalls != 1 {
		t.Fatalf("submit calls = %d, want 1", provider.submitCalls)
	}
	if provider.reconcileCalls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", provider.reconcileCalls)
	}
	if store.outcome.State != OutcomeAcceptedWithWarning {
		t.Fatalf("outcome state = %q, want %q", store.outcome.State, OutcomeAcceptedWithWarning)
	}
	if store.update.InvoiceUUID != inv.UUID {
		t.Fatalf("provider identity changed: %q != %q", store.update.InvoiceUUID, inv.UUID)
	}
	if errors.Is(store.outcomeError(), ErrDuplicateDelivery) {
		t.Fatal("reconciliation must not be treated as duplicate delivery")
	}
}

func TestNotFoundReusesProviderIdentityBeforeResubmitting(t *testing.T) {
	provider := &idempotencyProvider{
		submitResult: &ProviderResult{State: OutcomeAccepted, ProviderCode: "REPORTED"},
		submitErr:    &ProviderError{State: OutcomeAmbiguous, Code: "TIMEOUT", Message: "response lost"},
	}
	store := &idempotencyStore{}
	inv := outcomeTestInvoice()
	meta := outcomeTestMeta()
	meta.stableDocumentKey = "tenant-a:2:bill:41"

	if _, err := signSubmitUpdateWithProvider(provider, store, inv, 41, meta); err == nil {
		t.Fatal("first delivery unexpectedly succeeded")
	}
	provider.submitErr = nil
	provider.reconcileResult = nil
	if _, err := signSubmitUpdateWithProvider(provider, store, inv, 41, meta); err != nil {
		t.Fatalf("not-found retry: %v", err)
	}
	if provider.submitCalls != 2 {
		t.Fatalf("submit calls = %d, want 2", provider.submitCalls)
	}
	if provider.reconcileCalls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", provider.reconcileCalls)
	}
	if len(provider.submittedUUIDs) != 2 || provider.submittedUUIDs[0] != provider.submittedUUIDs[1] {
		t.Fatalf("provider identity changed across retry: %#v", provider.submittedUUIDs)
	}
}

func (s *idempotencyStore) outcomeError() error {
	if s.outcome.State.success() {
		return nil
	}
	return outcomeError(s.outcome)
}

var _ SubmissionProvider = (*idempotencyProvider)(nil)
var _ SubmissionReconciler = (*idempotencyProvider)(nil)
var _ submissionStore = (*idempotencyStore)(nil)
var _ submissionRetryStarter = (*idempotencyStore)(nil)
