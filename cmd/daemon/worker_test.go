package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/zatca-go/zatca/client"
	"github.com/zatca-go/zatca/queue"
)

func TestOnboardInvalidOTPIsDurablyFailedBeforeAck(t *testing.T) {
	store := &fakeOnboardingStateStore{}
	acked := false
	runner := fakeOnboardingRunner("compliance", &client.APIError{
		StatusCode: 401,
		Body:       `{"message":"invalid OTP"}`,
	})

	handleOnboardMessage(
		&tenantCtx{},
		queue.Message{DocType: queue.DocOnboard, BranchID: 42, DBName: "tenant"},
		store,
		runner,
		nil,
		func() error {
			acked = true
			return nil
		},
	)

	if !acked {
		t.Fatal("invalid OTP should be acknowledged after failed state is durable")
	}
	if store.failedCode != "invalid_otp" {
		t.Fatalf("failed code = %q, want invalid_otp", store.failedCode)
	}
	if store.failedAttempt != 1 {
		t.Fatalf("failed attempt = %d, want 1", store.failedAttempt)
	}
	if !store.completedBeforeAck {
		t.Fatal("failure state must be durable before ACK")
	}
}

func TestOnboardProviderOutageIsDurablyFailedBeforeAck(t *testing.T) {
	store := &fakeOnboardingStateStore{}
	acked := false
	runner := fakeOnboardingRunner("compliance", &client.APIError{
		StatusCode: 503,
		Body:       "provider unavailable",
	})

	handleOnboardMessage(
		&tenantCtx{},
		queue.Message{DocType: queue.DocOnboard, BranchID: 42, DBName: "tenant"},
		store,
		runner,
		nil,
		func() error {
			acked = true
			return nil
		},
	)

	if !acked {
		t.Fatal("provider outage should be acknowledged after failed state is durable")
	}
	if store.failedCode != "provider_unavailable" {
		t.Fatalf("failed code = %q, want provider_unavailable", store.failedCode)
	}
	if store.failedMessage == "" {
		t.Fatal("provider outage should retain bounded error evidence")
	}
}

func TestOnboardSuccessCompletesBeforeActivatingBranch(t *testing.T) {
	store := &fakeOnboardingStateStore{}
	activated := false

	handleOnboardMessage(
		&tenantCtx{},
		queue.Message{DocType: queue.DocOnboard, BranchID: 42, DBName: "tenant"},
		store,
		fakeOnboardingRunner("", nil),
		func(queue.Message) {
			if !store.completed {
				t.Error("branch activated before done state was durable")
			}
			activated = true
		},
		func() error { return nil },
	)

	if !activated {
		t.Fatal("successful onboarding should activate the branch")
	}
	if !store.completed {
		t.Fatal("successful onboarding should persist done state")
	}
	if store.failedCode != "" {
		t.Fatalf("unexpected failure code %q", store.failedCode)
	}
	if got, want := progressSteps(store.progress), []string{"csr", "compliance", "invoices", "production"}; !equalStrings(got, want) {
		t.Fatalf("progress steps = %v, want %v", got, want)
	}
}

func TestOnboardRetryUsesNewAttemptAfterFailure(t *testing.T) {
	store := &fakeOnboardingStateStore{}
	acked := 0
	failedRunner := fakeOnboardingRunner("compliance", errors.New("temporary provider outage"))
	successRunner := fakeOnboardingRunner("", nil)
	msg := queue.Message{DocType: queue.DocOnboard, BranchID: 42, DBName: "tenant"}

	handleOnboardMessage(&tenantCtx{}, msg, store, failedRunner, nil, func() error {
		acked++
		return nil
	})
	handleOnboardMessage(&tenantCtx{}, msg, store, successRunner, nil, func() error {
		acked++
		return nil
	})

	if acked != 2 {
		t.Fatalf("ack count = %d, want 2", acked)
	}
	if len(store.queuedAttempts) != 2 || store.queuedAttempts[0] != 1 || store.queuedAttempts[1] != 2 {
		t.Fatalf("queued attempts = %v, want [1 2]", store.queuedAttempts)
	}
	if store.failedAttempt != 1 {
		t.Fatalf("failed attempt = %d, want 1", store.failedAttempt)
	}
	if !store.completed || store.completedAttempt != 2 {
		t.Fatalf("completed attempt = %d, want 2", store.completedAttempt)
	}
}

func TestOnboardFailureIsNotAcknowledgedWhenFailureStatePersistenceFails(t *testing.T) {
	store := &fakeOnboardingStateStore{failErr: errors.New("database unavailable")}
	acked := false

	handleOnboardMessage(
		&tenantCtx{},
		queue.Message{DocType: queue.DocOnboard, BranchID: 42, DBName: "tenant"},
		store,
		fakeOnboardingRunner("compliance", errors.New("invalid OTP")),
		nil,
		func() error {
			acked = true
			return nil
		},
	)

	if acked {
		t.Fatal("message must remain unacknowledged when failed state cannot be persisted")
	}
}

type fakeOnboardingStateStore struct {
	nextAttempt        uint64
	queuedAttempts     []uint64
	progress           []onboardingProgress
	failedCode         string
	failedMessage      string
	failedAttempt      uint64
	completed          bool
	completedAttempt   uint64
	completedBeforeAck bool
	failErr            error
}

func (f *fakeOnboardingStateStore) Queue(int64) (uint64, error) {
	f.nextAttempt++
	f.queuedAttempts = append(f.queuedAttempts, f.nextAttempt)
	return f.nextAttempt, nil
}

func (f *fakeOnboardingStateStore) Progress(progress onboardingProgress) error {
	f.progress = append(f.progress, progress)
	return nil
}

func (f *fakeOnboardingStateStore) Fail(_ int64, attempt uint64, code, message string) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.failedAttempt = attempt
	f.failedCode = code
	f.failedMessage = message
	f.completedBeforeAck = true
	return nil
}

func (f *fakeOnboardingStateStore) Complete(_ int64, attempt uint64) error {
	f.completed = true
	f.completedAttempt = attempt
	return nil
}

func fakeOnboardingRunner(failStep string, runErr error) onboardingRunFunc {
	return func(_ *tenantCtx, _ queue.Message, progress func(string) error) error {
		for _, step := range []string{"csr", "compliance", "invoices", "production"} {
			if step == failStep {
				if runErr == nil {
					return fmt.Errorf("%s failed", step)
				}
				return runErr
			}
			if err := progress(step); err != nil {
				return err
			}
		}

		return runErr
	}
}

func progressSteps(progress []onboardingProgress) []string {
	steps := make([]string, 0, len(progress))
	for _, item := range progress {
		steps = append(steps, item.Step)
	}
	return steps
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
