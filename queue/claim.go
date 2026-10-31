package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ClaimDecision describes the durable identity decision for one delivery.
type ClaimDecision uint8

const (
	ClaimProcess ClaimDecision = iota
	ClaimDuplicate
	ClaimConflict
	ClaimInFlight
)

var (
	// ErrFingerprintConflict means that a request identity was reused with
	// different content. The delivery is acknowledged after the durable
	// original claim has been consulted.
	ErrFingerprintConflict = errors.New("queue request fingerprint conflict")
	// ErrClaimInFlight means another delivery currently owns the claim.
	ErrClaimInFlight = errors.New("queue claim is in flight")
	// ErrClaimNotFound means completion was attempted without a durable claim.
	ErrClaimNotFound = errors.New("queue claim not found")
)

// ClaimStore persists the business-effect claim used by a consumer.
type ClaimStore interface {
	Claim(context.Context, Message) (ClaimDecision, error)
	Complete(context.Context, Message) error
}

type memoryClaim struct {
	fingerprint  string
	state        string
	leaseExpires time.Time
}

type memoryEvent struct {
	requestKey  string
	fingerprint string
}

// MemoryClaimStore is a deterministic claim store for unit tests and local
// harnesses. Production workers use SQLClaimStore.
type MemoryClaimStore struct {
	mu            sync.Mutex
	claims        map[string]*memoryClaim
	events        map[string]memoryEvent
	LeaseDuration time.Duration
	Now           func() time.Time
}

// NewMemoryClaimStore returns an empty claim store with a lease longer than
// the daemon's maximum processing retry window.
func NewMemoryClaimStore() *MemoryClaimStore {
	return &MemoryClaimStore{
		claims:        make(map[string]*memoryClaim),
		events:        make(map[string]memoryEvent),
		LeaseDuration: defaultClaimLease,
	}
}

func (s *MemoryClaimStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *MemoryClaimStore) ensureMaps() {
	if s.claims == nil {
		s.claims = make(map[string]*memoryClaim)
	}
	if s.events == nil {
		s.events = make(map[string]memoryEvent)
	}
	if s.LeaseDuration == 0 {
		s.LeaseDuration = defaultClaimLease
	}
}

// Claim atomically claims a new request or returns the state of its existing
// request/event claim.
func (s *MemoryClaimStore) Claim(_ context.Context, msg Message) (ClaimDecision, error) {
	if err := msg.Validate(); err != nil {
		return ClaimConflict, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMaps()

	requestKey := msg.RequestKey()
	eventKey := msg.EventKey()
	fingerprint := msg.BusinessFingerprint()
	now := s.now()

	if event, ok := s.events[eventKey]; ok {
		if event.requestKey != requestKey || event.fingerprint != fingerprint {
			return ClaimConflict, nil
		}
		claim, ok := s.claims[requestKey]
		if !ok {
			return ClaimConflict, fmt.Errorf("%w: missing request claim for event_id %s", ErrClaimNotFound, msg.EventID)
		}
		if claim.fingerprint != fingerprint {
			return ClaimConflict, nil
		}
		return s.decisionForClaim(claim, now), nil
	}

	if claim, ok := s.claims[requestKey]; ok {
		if claim.fingerprint != fingerprint {
			s.events[eventKey] = memoryEvent{
				requestKey:  requestKey,
				fingerprint: fingerprint,
			}
			return ClaimConflict, nil
		}

		s.events[eventKey] = memoryEvent{
			requestKey:  requestKey,
			fingerprint: fingerprint,
		}
		return s.decisionForClaim(claim, now), nil
	}

	s.claims[requestKey] = &memoryClaim{
		fingerprint:  fingerprint,
		state:        "processing",
		leaseExpires: now.Add(s.LeaseDuration),
	}
	s.events[eventKey] = memoryEvent{
		requestKey:  requestKey,
		fingerprint: fingerprint,
	}
	return ClaimProcess, nil
}

func (s *MemoryClaimStore) decisionForClaim(claim *memoryClaim, now time.Time) ClaimDecision {
	if claim.state == "completed" {
		return ClaimDuplicate
	}
	if claim.leaseExpires.After(now) {
		return ClaimInFlight
	}
	claim.state = "processing"
	claim.leaseExpires = now.Add(s.LeaseDuration)
	return ClaimProcess
}

// Complete records the durable business-effect completion. It is idempotent
// so an ACK failure can safely be recovered by a later redelivery.
func (s *MemoryClaimStore) Complete(_ context.Context, msg Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMaps()

	claim, ok := s.claims[msg.RequestKey()]
	if !ok {
		return fmt.Errorf("%w: %s", ErrClaimNotFound, msg.RequestKey())
	}
	if claim.fingerprint != msg.BusinessFingerprint() {
		return fmt.Errorf("%w: bill_request_id %s", ErrFingerprintConflict, msg.BillRequestID)
	}
	if claim.state == "completed" {
		return nil
	}
	claim.state = "completed"
	claim.leaseExpires = time.Time{}
	return nil
}

// HandleDelivery decodes, claims, processes, durably completes, and then
// acknowledges one queue delivery. The ACK is deliberately last.
func HandleDelivery(
	ctx context.Context,
	data []byte,
	store ClaimStore,
	effect func(Message) error,
	ack func() error,
) error {
	msg, err := DecodeEnvelope(data)
	if err != nil {
		return err
	}
	return HandleMessage(ctx, msg, store, effect, ack)
}

// HandleMessage applies the delivery protocol to an already decoded message.
func HandleMessage(
	ctx context.Context,
	msg Message,
	store ClaimStore,
	effect func(Message) error,
	ack func() error,
) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	if store == nil {
		return errors.New("queue claim store is required")
	}
	if effect == nil {
		return errors.New("queue business effect is required")
	}
	if ack == nil {
		return errors.New("queue ACK function is required")
	}

	decision, err := store.Claim(ctx, msg)
	if err != nil {
		return err
	}
	switch decision {
	case ClaimDuplicate:
		return ack()
	case ClaimConflict:
		conflict := fmt.Errorf("%w: bill_request_id %s", ErrFingerprintConflict, msg.BillRequestID)
		if ackErr := ack(); ackErr != nil {
			return fmt.Errorf("%w; ACK failed: %v", conflict, ackErr)
		}
		return conflict
	case ClaimInFlight:
		return ErrClaimInFlight
	case ClaimProcess:
		// Continue below.
	default:
		return fmt.Errorf("unknown queue claim decision %d", decision)
	}

	if err := effect(msg); err != nil {
		return err
	}
	if err := store.Complete(ctx, msg); err != nil {
		return fmt.Errorf("complete queue claim: %w", err)
	}
	return ack()
}
