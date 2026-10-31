package processor

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type fakeSubmissionProvider struct {
	submit func(SubmissionRequest) (*ProviderResponse, error)
	lookup func(SubmissionIdentity) (*ProviderResponse, error)

	submits []SubmissionRequest
	lookups []SubmissionIdentity
}

func (f *fakeSubmissionProvider) Submit(_ context.Context, request SubmissionRequest) (*ProviderResponse, error) {
	f.submits = append(f.submits, request)
	if f.submit == nil {
		return nil, nil
	}
	return f.submit(request)
}

func (f *fakeSubmissionProvider) Lookup(_ context.Context, identity SubmissionIdentity) (*ProviderResponse, error) {
	f.lookups = append(f.lookups, identity)
	if f.lookup == nil {
		return nil, nil
	}
	return f.lookup(identity)
}

func testSubmissionRequest() SubmissionRequest {
	return SubmissionRequest{
		Identity: SubmissionIdentity{
			TenantID:      42,
			EGSSequenceID: "egs-1",
			BillRequestID: "bill-request-1",
			EventID:       "event-1",
			InvoiceUUID:   "invoice-uuid-1",
			PosID:         "pos-1",
			DocumentType:  "bill",
			SourceID:      7,
		},
		SignedRequest:   []byte("<signed>immutable</signed>"),
		ProviderRequest: []byte(`{"uuid":"invoice-uuid-1","invoiceHash":"hash-1"}`),
		InvoiceHash:     "hash-1",
		QRCode:          []byte("qr-1"),
	}
}

func newTestSubmissionEngine(provider SubmissionProvider, store SubmissionStore) *SubmissionEngine {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	engine := NewSubmissionEngine(provider, store)
	engine.Now = func() time.Time { return now }
	engine.BaseBackoff = time.Second
	engine.MaxBackoff = 4 * time.Second
	engine.MaxAttempts = 3
	return engine
}

func TestSubmissionProviderHTTP400IsRejectedWithoutRetry(t *testing.T) {
	provider := &fakeSubmissionProvider{
		submit: func(SubmissionRequest) (*ProviderResponse, error) {
			return nil, &ProviderHTTPError{
				StatusCode: http.StatusBadRequest,
				Body:       []byte(`{"code":"INVALID"}`),
			}
		},
	}
	store := NewMemorySubmissionStore()
	engine := newTestSubmissionEngine(provider, store)

	result, err := engine.Submit(context.Background(), testSubmissionRequest())
	if err == nil {
		t.Fatal("expected rejected outcome error")
	}
	var outcomeErr *SubmissionOutcomeError
	if !errors.As(err, &outcomeErr) {
		t.Fatalf("error = %T, want *SubmissionOutcomeError", err)
	}
	if outcomeErr.State != SubmissionRejected || outcomeErr.Retry != RetryNone {
		t.Fatalf("outcome = state %q retry %q, want rejected/none", outcomeErr.State, outcomeErr.Retry)
	}
	if result.Attempt.State != SubmissionRejected {
		t.Fatalf("attempt state = %q, want rejected", result.Attempt.State)
	}
	if len(provider.submits) != 1 || len(provider.lookups) != 0 {
		t.Fatalf("provider calls = submits %d lookups %d, want 1/0", len(provider.submits), len(provider.lookups))
	}
	if string(result.Attempt.ResponseArtifact) != `{"code":"INVALID"}` {
		t.Fatalf("response artifact = %q, want preserved HTTP body", result.Attempt.ResponseArtifact)
	}
}

func TestSubmissionProviderTransientHTTPStatusesReconcileBeforeRetry(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			provider := &fakeSubmissionProvider{}
			submitCount := 0
			lookupCount := 0
			provider.submit = func(SubmissionRequest) (*ProviderResponse, error) {
				submitCount++
				if submitCount == 1 {
					return nil, &ProviderHTTPError{StatusCode: status, Body: []byte("transient")}
				}
				return &ProviderResponse{Status: "ACCEPTED", ResponseArtifact: []byte(`{"status":"ACCEPTED"}`)}, nil
			}
			provider.lookup = func(SubmissionIdentity) (*ProviderResponse, error) {
				lookupCount++
				return &ProviderResponse{Found: false}, nil
			}

			store := NewMemorySubmissionStore()
			engine := newTestSubmissionEngine(provider, store)
			request := testSubmissionRequest()

			_, err := engine.Submit(context.Background(), request)
			if err == nil {
				t.Fatal("first transient attempt should be scheduled")
			}
			var firstErr *SubmissionOutcomeError
			if !errors.As(err, &firstErr) || firstErr.State != SubmissionAmbiguous || firstErr.Retry != RetryReconcile {
				t.Fatalf("first outcome = %v, want ambiguous/reconcile", err)
			}

			now := time.Date(2026, 10, 31, 12, 0, 2, 0, time.UTC)
			engine.Now = func() time.Time { return now }
			result, err := engine.Submit(context.Background(), request)
			if err != nil {
				t.Fatalf("reconciled retry error: %v", err)
			}
			if result.Attempt.State != SubmissionAccepted {
				t.Fatalf("final state = %q, want accepted", result.Attempt.State)
			}
			if submitCount != 2 || lookupCount != 1 {
				t.Fatalf("provider calls = submits %d lookups %d, want 2/1", submitCount, lookupCount)
			}
		})
	}
}

func TestSubmissionProviderTimeoutAfterAcceptanceUsesLookup(t *testing.T) {
	provider := &fakeSubmissionProvider{
		submit: func(SubmissionRequest) (*ProviderResponse, error) {
			return nil, context.DeadlineExceeded
		},
		lookup: func(SubmissionIdentity) (*ProviderResponse, error) {
			return &ProviderResponse{
				Found:            true,
				Status:           "CLEARED",
				ResponseArtifact: []byte(`{"status":"CLEARED"}`),
				ClearedArtifact:  []byte("<cleared/>"),
			}, nil
		},
	}
	store := NewMemorySubmissionStore()
	engine := newTestSubmissionEngine(provider, store)

	_, err := engine.Submit(context.Background(), testSubmissionRequest())
	if err == nil {
		t.Fatal("timeout should be ambiguous until reconciliation")
	}
	engine.Now = func() time.Time {
		return time.Date(2026, 10, 31, 12, 0, 2, 0, time.UTC)
	}
	result, err := engine.Submit(context.Background(), testSubmissionRequest())
	if err != nil {
		t.Fatalf("lookup should resolve timeout: %v", err)
	}
	if result.Attempt.State != SubmissionAccepted {
		t.Fatalf("state = %q, want accepted", result.Attempt.State)
	}
	if len(provider.submits) != 1 || len(provider.lookups) != 1 {
		t.Fatalf("provider calls = submits %d lookups %d, want 1/1", len(provider.submits), len(provider.lookups))
	}
	if string(result.Attempt.ClearedArtifact) != "<cleared/>" {
		t.Fatalf("cleared artifact = %q, want preserved", result.Attempt.ClearedArtifact)
	}
}

func TestSubmissionProviderNilResponseIsAmbiguous(t *testing.T) {
	provider := &fakeSubmissionProvider{
		submit: func(SubmissionRequest) (*ProviderResponse, error) {
			return nil, nil
		},
	}
	store := NewMemorySubmissionStore()
	engine := newTestSubmissionEngine(provider, store)

	result, err := engine.Submit(context.Background(), testSubmissionRequest())
	if err == nil {
		t.Fatal("nil provider response should be ambiguous")
	}
	var outcomeErr *SubmissionOutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.State != SubmissionAmbiguous {
		t.Fatalf("error = %v, want ambiguous outcome", err)
	}
	if result.Attempt.State != SubmissionAmbiguous || len(provider.lookups) != 0 {
		t.Fatalf("state/lookups = %q/%d, want ambiguous/0", result.Attempt.State, len(provider.lookups))
	}
}

func TestSubmissionProviderDuplicateDeliveryDoesNotResubmit(t *testing.T) {
	provider := &fakeSubmissionProvider{
		submit: func(SubmissionRequest) (*ProviderResponse, error) {
			return &ProviderResponse{Status: "REPORTED", ResponseArtifact: []byte(`{"status":"REPORTED"}`)}, nil
		},
	}
	store := NewMemorySubmissionStore()
	engine := newTestSubmissionEngine(provider, store)
	request := testSubmissionRequest()

	first, err := engine.Submit(context.Background(), request)
	if err != nil {
		t.Fatalf("first submit error: %v", err)
	}
	second, err := engine.Submit(context.Background(), request)
	if err != nil {
		t.Fatalf("duplicate submit error: %v", err)
	}
	if !second.Duplicate {
		t.Fatal("duplicate delivery should be marked duplicate")
	}
	if len(provider.submits) != 1 {
		t.Fatalf("submit calls = %d, want 1", len(provider.submits))
	}
	if string(second.Attempt.RequestArtifact) != string(first.Attempt.RequestArtifact) {
		t.Fatal("duplicate delivery changed the immutable request artifact")
	}
}

func TestSubmissionAttemptArtifactsRemainImmutableAcrossRetry(t *testing.T) {
	provider := &fakeSubmissionProvider{}
	submitCount := 0
	provider.submit = func(SubmissionRequest) (*ProviderResponse, error) {
		submitCount++
		if submitCount == 1 {
			return nil, &ProviderHTTPError{StatusCode: http.StatusServiceUnavailable, Body: []byte("overloaded")}
		}
		return &ProviderResponse{
			Status:           "ACCEPTED_WITH_WARNING",
			ResponseArtifact: []byte(`{"status":"ACCEPTED_WITH_WARNING"}`),
		}, nil
	}
	provider.lookup = func(SubmissionIdentity) (*ProviderResponse, error) {
		return &ProviderResponse{Found: false}, nil
	}

	store := NewMemorySubmissionStore()
	engine := newTestSubmissionEngine(provider, store)
	request := testSubmissionRequest()

	_, _ = engine.Submit(context.Background(), request)
	engine.Now = func() time.Time {
		return time.Date(2026, 10, 31, 12, 0, 2, 0, time.UTC)
	}
	if _, err := engine.Submit(context.Background(), request); err != nil {
		t.Fatalf("retry error: %v", err)
	}

	attempts := store.Attempts(request.Identity)
	if len(attempts) != 3 {
		t.Fatalf("attempt count = %d, want submission + reconciliation + retry", len(attempts))
	}
	if string(attempts[0].SignedRequest) != "<signed>immutable</signed>" ||
		string(attempts[0].RequestArtifact) != string(request.ProviderRequest) ||
		string(attempts[0].ResponseArtifact) != "overloaded" {
		t.Fatalf("first attempt artifacts were not preserved: %+v", attempts[0])
	}
	if string(attempts[2].SignedRequest) != string(attempts[0].SignedRequest) ||
		string(attempts[2].RequestArtifact) != string(attempts[0].RequestArtifact) {
		t.Fatal("retry rebuilt or changed the immutable request artifact")
	}
}

func TestSubmissionProviderDoesNotBlindRetryWhenLookupIsUnavailable(t *testing.T) {
	provider := &fakeSubmissionProvider{
		submit: func(SubmissionRequest) (*ProviderResponse, error) {
			return nil, &ProviderHTTPError{StatusCode: http.StatusInternalServerError, Body: []byte("unknown")}
		},
		lookup: func(SubmissionIdentity) (*ProviderResponse, error) {
			return nil, errors.New("lookup unavailable")
		},
	}
	store := NewMemorySubmissionStore()
	engine := newTestSubmissionEngine(provider, store)
	request := testSubmissionRequest()

	_, _ = engine.Submit(context.Background(), request)
	engine.Now = func() time.Time {
		return time.Date(2026, 10, 31, 12, 0, 2, 0, time.UTC)
	}
	result, err := engine.Submit(context.Background(), request)
	if err == nil {
		t.Fatal("lookup failure should keep the outcome ambiguous")
	}
	var outcomeErr *SubmissionOutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.State != SubmissionAmbiguous || outcomeErr.Retry != RetryReconcile {
		t.Fatalf("outcome = %v, want ambiguous/reconcile", err)
	}
	if len(provider.submits) != 1 || len(provider.lookups) != 1 {
		t.Fatalf("provider calls = submits %d lookups %d, want 1/1", len(provider.submits), len(provider.lookups))
	}
	if !result.Attempt.ManualReview {
		t.Fatal("lookup failure should require manual review until reconciliation succeeds")
	}
}
