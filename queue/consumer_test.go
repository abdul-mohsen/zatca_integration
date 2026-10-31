package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestDuplicateEventDeliveryProducesOneBusinessEffect(t *testing.T) {
	store := NewMemoryClaimStore()
	msg := canonicalTestMessage(42, "pos-1", "request-1", "event-1", "egs-1", "fingerprint-1")
	payload, err := Encode(msg)
	if err != nil {
		t.Fatal(err)
	}

	var effects, acks int
	effect := func(Message) error {
		effects++
		return nil
	}
	ack := func() error {
		acks++
		return nil
	}

	if err := HandleDelivery(context.Background(), payload, store, effect, ack); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := HandleDelivery(context.Background(), payload, store, effect, ack); err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}

	if effects != 1 {
		t.Fatalf("business effects = %d, want 1", effects)
	}
	if acks != 2 {
		t.Fatalf("acks = %d, want 2", acks)
	}
}

func TestSameRequestReplayWithNewEventIDDoesNotRepeatBusinessEffect(t *testing.T) {
	store := NewMemoryClaimStore()
	first := canonicalTestMessage(42, "pos-1", "request-1", "event-1", "egs-1", "fingerprint-1")
	replay := first
	replay.EventID = "event-2"

	var effects int
	effect := func(Message) error {
		effects++
		return nil
	}

	for _, msg := range []Message{first, replay} {
		payload, err := Encode(msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := HandleDelivery(context.Background(), payload, store, effect, func() error { return nil }); err != nil {
			t.Fatalf("delivery %s: %v", msg.EventID, err)
		}
	}

	if effects != 1 {
		t.Fatalf("business effects = %d, want 1", effects)
	}
}

func TestChangedFingerprintConflictsWithoutBusinessEffect(t *testing.T) {
	store := NewMemoryClaimStore()
	first := canonicalTestMessage(42, "pos-1", "request-1", "event-1", "egs-1", "fingerprint-1")
	conflict := first
	conflict.EventID = "event-2"
	conflict.RequestFingerprint = "fingerprint-2"

	firstPayload, err := Encode(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := HandleDelivery(context.Background(), firstPayload, store, func(Message) error { return nil }, func() error { return nil }); err != nil {
		t.Fatalf("first delivery: %v", err)
	}

	var effects, acks int
	conflictPayload, err := Encode(conflict)
	if err != nil {
		t.Fatal(err)
	}
	err = HandleDelivery(
		context.Background(),
		conflictPayload,
		store,
		func(Message) error {
			effects++
			return nil
		},
		func() error {
			acks++
			return nil
		},
	)
	if !errors.Is(err, ErrFingerprintConflict) {
		t.Fatalf("error = %v, want ErrFingerprintConflict", err)
	}
	if effects != 0 {
		t.Fatalf("business effects = %d, want 0", effects)
	}
	if acks != 1 {
		t.Fatalf("acks = %d, want 1 after durable conflict classification", acks)
	}
}

func TestACKWaitsForDurableCompletionAfterCrashWindow(t *testing.T) {
	store := &failCompleteStore{
		inner:  NewMemoryClaimStore(),
		failOn: 1,
	}
	msg := canonicalTestMessage(42, "pos-1", "request-1", "event-1", "egs-1", "fingerprint-1")
	payload, err := Encode(msg)
	if err != nil {
		t.Fatal(err)
	}

	var acks int
	err = HandleDelivery(
		context.Background(),
		payload,
		store,
		func(Message) error { return nil },
		func() error {
			acks++
			return nil
		},
	)
	if err == nil {
		t.Fatal("first delivery succeeded despite completion failure")
	}
	if acks != 0 {
		t.Fatalf("acks = %d, want 0 before durable completion", acks)
	}
}

func TestRedeliveryAfterACKCrashDoesNotRepeatBusinessEffect(t *testing.T) {
	store := NewMemoryClaimStore()
	msg := canonicalTestMessage(42, "pos-1", "request-1", "event-1", "egs-1", "fingerprint-1")
	payload, err := Encode(msg)
	if err != nil {
		t.Fatal(err)
	}

	var effects, acks int
	firstAck := true
	ack := func() error {
		acks++
		if firstAck {
			firstAck = false
			return errors.New("simulated crash during ACK")
		}
		return nil
	}
	effect := func(Message) error {
		effects++
		return nil
	}

	if err := HandleDelivery(context.Background(), payload, store, effect, ack); err == nil {
		t.Fatal("first delivery succeeded despite ACK failure")
	}
	if err := HandleDelivery(context.Background(), payload, store, effect, ack); err != nil {
		t.Fatalf("redelivery after ACK failure: %v", err)
	}
	if effects != 1 {
		t.Fatalf("business effects = %d, want 1", effects)
	}
	if acks != 2 {
		t.Fatalf("acks = %d, want 2", acks)
	}
}

func TestClaimKeyIncludesTenantID(t *testing.T) {
	store := NewMemoryClaimStore()
	tenantOne := canonicalTestMessage(1, "pos-1", "request-1", "event-1", "egs-1", "fingerprint-1")
	tenantTwo := canonicalTestMessage(2, "pos-1", "request-1", "event-1", "egs-1", "fingerprint-1")

	var mu sync.Mutex
	effects := 0
	effect := func(Message) error {
		mu.Lock()
		defer mu.Unlock()
		effects++
		return nil
	}
	for _, msg := range []Message{tenantOne, tenantTwo} {
		payload, err := Encode(msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := HandleDelivery(context.Background(), payload, store, effect, func() error { return nil }); err != nil {
			t.Fatalf("tenant %d delivery: %v", msg.TenantID, err)
		}
	}

	if effects != 2 {
		t.Fatalf("business effects = %d, want 2", effects)
	}
}

func TestDecodeCanonicalEnvelopeNormalizesDeliveryIDForReplayFingerprint(t *testing.T) {
	first := canonicalTestMessage(42, "pos-1", "request-1", "event-1", "egs-1", "")
	replay := first
	replay.EventID = "event-2"

	firstPayload, err := Encode(first)
	if err != nil {
		t.Fatal(err)
	}
	replayPayload, err := Encode(replay)
	if err != nil {
		t.Fatal(err)
	}
	decodedFirst, err := DecodeEnvelope(firstPayload)
	if err != nil {
		t.Fatalf("decode first: %v", err)
	}
	decodedReplay, err := DecodeEnvelope(replayPayload)
	if err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if decodedFirst.BusinessFingerprint() != decodedReplay.BusinessFingerprint() {
		t.Fatalf("replay fingerprint differs: %s vs %s",
			decodedFirst.BusinessFingerprint(), decodedReplay.BusinessFingerprint())
	}
}

func TestInvalidEnvelopeIsNotAcknowledged(t *testing.T) {
	var acks int
	err := HandleDelivery(
		context.Background(),
		[]byte(`{"doc_type":"bill","id":99}`),
		NewMemoryClaimStore(),
		func(Message) error {
			t.Fatal("invalid envelope reached business effect")
			return nil
		},
		func() error {
			acks++
			return nil
		},
	)
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("error = %v, want ErrInvalidEnvelope", err)
	}
	if acks != 0 {
		t.Fatalf("acks = %d, want 0", acks)
	}
}

func canonicalTestMessage(tenantID uint64, posID, requestID, eventID, egsID, fingerprint string) Message {
	return Message{
		DocType:            DocBill,
		ID:                 99,
		BranchID:           7,
		DBName:             fmt.Sprintf("tenant_%d", tenantID),
		TenantID:           tenantID,
		PosID:              posID,
		BillRequestID:      requestID,
		EventID:            eventID,
		EGSSequenceID:      egsID,
		InvoiceUUID:        "invoice-" + requestID,
		RequestFingerprint: fingerprint,
	}
}

type failCompleteStore struct {
	inner  ClaimStore
	failOn int
	calls  int
}

func (s *failCompleteStore) Claim(ctx context.Context, msg Message) (ClaimDecision, error) {
	return s.inner.Claim(ctx, msg)
}

func (s *failCompleteStore) Complete(ctx context.Context, msg Message) error {
	s.calls++
	if s.calls == s.failOn {
		return errors.New("simulated crash before durable completion")
	}
	return s.inner.Complete(ctx, msg)
}
