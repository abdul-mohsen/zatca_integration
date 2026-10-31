package queue

import (
	"context"
	"errors"
	"testing"
)

type preflightSequenceStore struct {
	reserves int
	commits  int
}

func (s *preflightSequenceStore) Reserve(context.Context, EGSSequenceReservationRequest) (EGSSequenceReservation, error) {
	s.reserves++
	return EGSSequenceReservation{}, nil
}

func (s *preflightSequenceStore) Commit(context.Context, EGSSequenceCommitRequest) error {
	s.commits++
	return nil
}

func (s *preflightSequenceStore) Release(context.Context, EGSSequenceReservation) error {
	return nil
}

func TestOrderedStreamCredentialPreflightRunsBeforeICVProviderAndACK(t *testing.T) {
	claims := NewMemoryClaimStore()
	chain := &preflightSequenceStore{}
	var effects, acks int
	preflightErr := errors.New("credential action required")
	stream := NewOrderedStreamWithPreflight(
		claims,
		chain,
		func(context.Context, Message) (context.Context, error) {
			return context.Background(), preflightErr
		},
		func(context.Context, Message, EGSSequenceReservation) (EGSSequenceCommitRequest, error) {
			effects++
			return EGSSequenceCommitRequest{}, nil
		},
	)
	msg := orderedTestMessage(DocBill, 7, "egs-a", "request-preflight", "event-preflight", "uuid-preflight")

	var err error
	err = stream.HandleMessage(context.Background(), msg, func() error {
		acks++
		return nil
	})
	if !errors.Is(err, preflightErr) {
		t.Fatalf("HandleMessage() error = %v, want preflight error", err)
	}
	if chain.reserves != 0 {
		t.Fatalf("ICV reservations = %d, want 0", chain.reserves)
	}
	if effects != 0 {
		t.Fatalf("provider/business effects = %d, want 0", effects)
	}
	if acks != 0 {
		t.Fatalf("ACKs = %d, want 0", acks)
	}
}
