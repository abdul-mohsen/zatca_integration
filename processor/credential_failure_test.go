package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zatca-go/zatca/credential"
)

type fakeCredentialFailureRecorder struct {
	identity credential.Identity
	cause    error
	calls    int
}

func (r *fakeCredentialFailureRecorder) RecordCredentialFailure(_ context.Context, identity credential.Identity, cause error) error {
	r.identity = identity
	r.cause = cause
	r.calls++
	return nil
}

func TestRunCredentialPreflightPersistsFailure(t *testing.T) {
	identity := credential.Identity{
		TenantID:       42,
		BranchID:       7,
		EGSSequenceID:  "egs-7",
		CredentialType: credential.CredentialTypeProduction,
	}
	recorder := &fakeCredentialFailureRecorder{}
	gate := &credential.PreflightGate{
		Now:      func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) },
		SDKReady: func() error { return nil },
	}
	input := credential.PreflightInput{
		Identity: identity,
		State: credential.State{
			Identity: identity,
			Status:   credential.StatusRevoked,
		},
	}

	err := RunCredentialPreflight(context.Background(), gate, input, recorder)
	if err == nil {
		t.Fatal("RunCredentialPreflight() error = nil, want failure")
	}
	if !errors.Is(err, credential.ErrCredentialRevoked) {
		t.Fatalf("error = %v, want ErrCredentialRevoked", err)
	}
	if recorder.calls != 1 || recorder.identity != identity || recorder.cause == nil {
		t.Fatalf("recorder = %#v, want one persisted failure for identity", recorder)
	}
}

func TestRunCredentialPreflightPersistsInvalidInput(t *testing.T) {
	gate := &credential.PreflightGate{
		SDKReady:  func() error { return nil },
		PathCheck: func(string) error { return nil },
	}
	input := credential.PreflightInput{}
	recorder := &fakeCredentialFailureRecorder{}

	if err := RunCredentialPreflight(context.Background(), gate, input, recorder); err == nil {
		t.Fatal("invalid empty input unexpectedly passed")
	}
	if recorder.calls != 1 {
		t.Fatalf("recorder calls = %d, want 1", recorder.calls)
	}
}
