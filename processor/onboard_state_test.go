package processor

import (
	"context"
	"errors"
	"testing"
)

type fakeOnboardingStateStore struct {
	states []OnboardingState
	errors []string
}

func (s *fakeOnboardingStateStore) SetOnboardingState(_ context.Context, _ int64, state OnboardingState, lastError string) error {
	s.states = append(s.states, state)
	s.errors = append(s.errors, lastError)
	return nil
}

func TestRunTrackedOnboardingPersistsSuccessfulTransitions(t *testing.T) {
	store := &fakeOnboardingStateStore{}
	err := RunTrackedOnboarding(context.Background(), 7, store, func(progress func(OnboardingState) error) error {
		if err := progress(OnboardingStateCompliance); err != nil {
			return err
		}
		return progress(OnboardingStateInvoices)
	})
	if err != nil {
		t.Fatalf("RunTrackedOnboarding() error = %v", err)
	}

	want := []OnboardingState{
		OnboardingStateCSR,
		OnboardingStateCompliance,
		OnboardingStateInvoices,
		OnboardingStateDone,
	}
	if len(store.states) != len(want) {
		t.Fatalf("states = %#v, want %#v", store.states, want)
	}
	for i := range want {
		if store.states[i] != want[i] {
			t.Fatalf("states[%d] = %q, want %q", i, store.states[i], want[i])
		}
	}
}

func TestRunTrackedOnboardingPersistsFailedTransition(t *testing.T) {
	store := &fakeOnboardingStateStore{}
	providerErr := errors.New("provider rejected OTP")
	err := RunTrackedOnboarding(context.Background(), 7, store, func(progress func(OnboardingState) error) error {
		if err := progress(OnboardingStateCompliance); err != nil {
			return err
		}
		return providerErr
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("RunTrackedOnboarding() error = %v, want provider error", err)
	}
	if got := store.states[len(store.states)-1]; got != OnboardingStateFailed {
		t.Fatalf("final state = %q, want %q", got, OnboardingStateFailed)
	}
	if got := store.errors[len(store.errors)-1]; got != providerErr.Error() {
		t.Fatalf("failure error = %q, want %q", got, providerErr.Error())
	}
}
