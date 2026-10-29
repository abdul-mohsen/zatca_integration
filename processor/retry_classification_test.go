package processor

import (
	"errors"
	"net"
	"testing"

	"github.com/zatca-go/zatca/client"
	"github.com/zatca-go/zatca/models"
)

func TestProviderErrorRetryClassification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantState OutcomeState
		wantRetry bool
	}{
		{name: "bad request is permanent", status: 400, wantState: OutcomeRejected, wantRetry: false},
		{name: "unauthorized is permanent", status: 401, wantState: OutcomeRejected, wantRetry: false},
		{name: "request timeout is transient", status: 408, wantState: OutcomeAmbiguous, wantRetry: true},
		{name: "conflict is transient", status: 409, wantState: OutcomeAmbiguous, wantRetry: true},
		{name: "rate limit is transient", status: 429, wantState: OutcomeAmbiguous, wantRetry: true},
		{name: "server error is transient", status: 500, wantState: OutcomeAmbiguous, wantRetry: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyProviderError(&client.APIError{StatusCode: tt.status, Body: "fake"})
			providerErr, ok := err.(*ProviderError)
			if !ok {
				t.Fatalf("error type = %T, want *ProviderError", err)
			}
			if providerErr.State != tt.wantState {
				t.Fatalf("state = %q, want %q", providerErr.State, tt.wantState)
			}
			if got := IsRetryableError(err); got != tt.wantRetry {
				t.Fatalf("retryable = %t, want %t", got, tt.wantRetry)
			}
		})
	}
}

func TestTimeoutAndTransportErrorsAreRetryable(t *testing.T) {
	timeoutErr := &fakeTimeoutError{}
	if !IsRetryableError(classifyProviderError(timeoutErr)) {
		t.Fatal("timeout error should be retryable")
	}
	if !IsRetryableError(classifyProviderError(net.ErrClosed)) {
		t.Fatal("transport error should be retryable")
	}
}

func TestTerminalProviderErrorIsNotRetryable(t *testing.T) {
	err := &ProviderError{
		State:   OutcomeTerminalFailure,
		Code:    "INVALID_SIGNED_XML",
		Message: "malformed XML",
	}
	if IsRetryableError(err) {
		t.Fatal("terminal provider error should not be retryable")
	}
}

func TestInvalidSignedXMLIsTerminalAndNotRetried(t *testing.T) {
	provider := invalidXMLProvider{}
	store := &fakeSubmissionStore{}

	_, err := signSubmitUpdateWithProvider(provider, store, outcomeTestInvoice(), 41, outcomeTestMeta())
	if err == nil {
		t.Fatal("expected invalid XML error")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error type = %T, want *ProviderError", err)
	}
	if providerErr.State != OutcomeTerminalFailure || IsRetryableError(providerErr) {
		t.Fatalf("provider error = %#v, want terminal non-retryable", providerErr)
	}
	if store.outcome.State != OutcomeTerminalFailure {
		t.Fatalf("durable state = %q, want %q", store.outcome.State, OutcomeTerminalFailure)
	}
	if store.outcome.NextRetryAt != nil {
		t.Fatal("invalid XML must not schedule a retry")
	}
}

func TestRetryExhaustionPersistsTerminalFailure(t *testing.T) {
	provider := fakeSubmissionProvider{
		err: &ProviderError{
			State:     OutcomeAmbiguous,
			Code:      "TIMEOUT",
			Message:   "response lost",
			Retryable: true,
		},
	}
	store := &fakeSubmissionStore{}
	meta := outcomeTestMeta()
	meta.deadLetter = true
	meta.retryAttempt = 3

	_, err := signSubmitUpdateWithProvider(provider, store, outcomeTestInvoice(), 41, meta)
	if err == nil {
		t.Fatal("expected exhausted retry error")
	}
	if store.outcome.State != OutcomeTerminalFailure {
		t.Fatalf("durable state = %q, want %q", store.outcome.State, OutcomeTerminalFailure)
	}
	if store.outcome.ErrorCode != "RETRY_EXHAUSTED" {
		t.Fatalf("error code = %q, want RETRY_EXHAUSTED", store.outcome.ErrorCode)
	}
	if store.outcome.NextRetryAt != nil {
		t.Fatal("exhausted retry must not schedule another retry")
	}
}

func TestTransientFailurePersistsRetryMetadata(t *testing.T) {
	provider := fakeSubmissionProvider{
		err: &ProviderError{
			State:     OutcomeAmbiguous,
			Code:      "TIMEOUT",
			Message:   "response lost",
			Retryable: true,
		},
	}
	store := &fakeSubmissionStore{}

	_, err := signSubmitUpdateWithProvider(provider, store, outcomeTestInvoice(), 41, outcomeTestMeta())
	if err == nil {
		t.Fatal("expected transient failure")
	}
	if store.outcome.State != OutcomeAmbiguous {
		t.Fatalf("durable state = %q, want %q", store.outcome.State, OutcomeAmbiguous)
	}
	if store.outcome.RetryCount != 0 {
		t.Fatalf("retry count = %d, want 0", store.outcome.RetryCount)
	}
	if store.outcome.NextRetryAt == nil {
		t.Fatal("transient failure should schedule a retry")
	}
}

func TestProductionReconciliationReportsLookupLimitation(t *testing.T) {
	provider := serviceSubmissionProvider{}
	_, err := provider.Reconcile(ProviderIdentity{
		DocumentID:  "doc-41",
		InvoiceHash: "hash-41",
		StableKey:   "tenant-a:2:bill:41",
	}, false)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error type = %T, want *ProviderError", err)
	}
	if providerErr.Code != "RECONCILIATION_UNSUPPORTED" {
		t.Fatalf("error code = %q, want RECONCILIATION_UNSUPPORTED", providerErr.Code)
	}
	if providerErr.State != OutcomeAmbiguous || !IsRetryableError(providerErr) {
		t.Fatalf("provider error = %#v, want retryable ambiguous", providerErr)
	}
}

type invalidXMLProvider struct{}

func (invalidXMLProvider) SignInvoice(string) (string, string, error) {
	return "<signed-invoice>", "hash-41", nil
}

func (invalidXMLProvider) GenerateAPIRequest(string) (models.InvoiceRequest, error) {
	return models.InvoiceRequest{}, nil
}

func (invalidXMLProvider) GenerateQR(string) (string, error) {
	return "", nil
}

func (invalidXMLProvider) Submit(models.InvoiceRequest, bool) (*ProviderResult, error) {
	return nil, nil
}

var _ SubmissionProvider = invalidXMLProvider{}

type fakeTimeoutError struct{}

func (e *fakeTimeoutError) Error() string   { return "timeout" }
func (e *fakeTimeoutError) Timeout() bool   { return true }
func (e *fakeTimeoutError) Temporary() bool { return true }

var _ net.Error = (*fakeTimeoutError)(nil)
