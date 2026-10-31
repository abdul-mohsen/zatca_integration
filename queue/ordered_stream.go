package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	// ErrEGSSequenceBusy means another document currently owns the stream
	// reservation and its lease has not expired.
	ErrEGSSequenceBusy = errors.New("EGS sequence reservation is busy")
	// ErrEGSSequenceOwnership means a reservation or commit does not belong to
	// the canonical tenant/EGS identity that created it.
	ErrEGSSequenceOwnership = errors.New("EGS sequence ownership mismatch")
	// ErrEGSSequenceReservationConflict means an identity was reused for a
	// different immutable document or outbox event.
	ErrEGSSequenceReservationConflict = errors.New("EGS sequence reservation identity conflict")
	// ErrEGSSequenceCommitNotApplied means the backend chain guard did not
	// advance the sequence.
	ErrEGSSequenceCommitNotApplied = errors.New("EGS sequence commit was not applied")
)

const defaultEGSSequenceLease = 5 * time.Minute

const (
	EGSDocumentQueued              = "queued"
	EGSDocumentSubmitted           = "submitted"
	EGSDocumentAccepted            = "accepted"
	EGSDocumentAcceptedWithWarning = "accepted_with_warning"
	// EGSDocumentWarning is retained as a source-compatible alias. New
	// records use the explicit accepted_with_warning state.
	EGSDocumentWarning   = EGSDocumentAcceptedWithWarning
	EGSDocumentRejected  = "rejected"
	EGSDocumentAmbiguous = "ambiguous"
	EGSDocumentTerminal  = "terminal"
)

// EGSSequenceIdentity is the canonical owner of one ordered invoice hash
// chain. User, company, and POS identities are deliberately not part of the
// partition key.
type EGSSequenceIdentity struct {
	EGSSequenceID string
	TenantID      uint64
	BranchID      int64
	VATNumber     string
	EGSID         string
}

// EGSSequenceReservationRequest describes the immutable identity used to
// reserve the next ICV in one EGS stream.
type EGSSequenceReservationRequest struct {
	EGSSequenceIdentity
	ReservationKey   string
	ReservationToken string
	DocumentUUID     string
	OutboxEventID    string
	LeaseDuration    time.Duration
}

// EGSSequenceReservation is the state that must be used to build a document.
// PIH is copied from the committed chain before the reservation is made.
type EGSSequenceReservation struct {
	EGSSequenceIdentity
	ReservationKey   string
	ReservationToken string
	DocumentUUID     string
	OutboxEventID    string
	ICV              uint64
	PIH              *string
	LeaseExpiresAt   time.Time
	Committed        bool
}

// EGSSequenceCommitRequest contains the immutable source snapshot and the
// signed document hash that advance the reserved stream.
type EGSSequenceCommitRequest struct {
	Reservation  EGSSequenceReservation
	DocumentType string
	SourceID     uint64
	State        string
	Snapshot     []byte
	DocumentHash string
}

// EGSSequenceDocument is the durable state recorded for one source document.
// It is also the shape used by the in-memory implementation's test harness.
type EGSSequenceDocument struct {
	EGSSequenceID string
	TenantID      uint64
	BranchID      int64
	VATNumber     string
	EGSID         string
	DocumentUUID  string
	DocumentType  string
	SourceID      uint64
	OutboxEventID string
	ICV           uint64
	PIH           *string
	DocumentHash  string
	Snapshot      []byte
	State         string
}

// EGSSequenceStore is the backend chain contract consumed by the ordered
// stream. Reserve and Commit are intentionally separate so a crash before
// commit leaves the same leased reservation available for replay.
type EGSSequenceStore interface {
	Reserve(context.Context, EGSSequenceReservationRequest) (EGSSequenceReservation, error)
	Commit(context.Context, EGSSequenceCommitRequest) error
	Release(context.Context, EGSSequenceReservation) error
}

// OrderedEffect builds an immutable source snapshot using the reserved UUID,
// ICV, and PIH. The ordered stream commits the returned snapshot only after
// the effect succeeds.
type OrderedEffect func(context.Context, Message, EGSSequenceReservation) (EGSSequenceCommitRequest, error)

// OrderedStream serializes all document types in one canonical
// (tenant_id, egs_sequence_id) partition while allowing different EGS
// partitions to make progress concurrently.
type OrderedStream struct {
	claims EGSSequenceStoreClaims
	chain  EGSSequenceStore
	effect OrderedEffect

	mu        sync.Mutex
	partition map[string]*sync.Mutex
}

// EGSSequenceStoreClaims is the subset of ClaimStore used by OrderedStream.
// Keeping the alias explicit makes the queue protocol easy to test without a
// NATS dependency.
type EGSSequenceStoreClaims interface {
	Claim(context.Context, Message) (ClaimDecision, error)
	Complete(context.Context, Message) error
}

// NewOrderedStream creates a combined bill/credit/debit consumer.
func NewOrderedStream(claims EGSSequenceStoreClaims, chain EGSSequenceStore, effect OrderedEffect) *OrderedStream {
	return &OrderedStream{
		claims:    claims,
		chain:     chain,
		effect:    effect,
		partition: make(map[string]*sync.Mutex),
	}
}

func (s *OrderedStream) lockFor(partition string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lock, ok := s.partition[partition]; ok {
		return lock
	}
	lock := &sync.Mutex{}
	s.partition[partition] = lock
	return lock
}

// HandleDelivery decodes and processes one combined-stream delivery.
func (s *OrderedStream) HandleDelivery(
	ctx context.Context,
	data []byte,
	ack func() error,
) error {
	msg, err := DecodeCanonicalEnvelope(data)
	if err != nil {
		return err
	}
	return s.HandleMessage(ctx, msg, ack)
}

// HandleMessage claims, reserves, effects, commits, completes, and finally
// ACKs one delivery. The partition lock is held across the entire protocol so
// document-type workers cannot advance independent chains.
func (s *OrderedStream) HandleMessage(
	ctx context.Context,
	msg Message,
	ack func() error,
) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	if s == nil || s.claims == nil {
		return errors.New("ordered stream claim store is required")
	}
	if s.chain == nil {
		return errors.New("ordered stream chain store is required")
	}
	if s.effect == nil {
		return errors.New("ordered stream business effect is required")
	}
	if ack == nil {
		return errors.New("ordered stream ACK function is required")
	}

	partitionLock := s.lockFor(msg.PartitionKey())
	partitionLock.Lock()
	defer partitionLock.Unlock()

	decision, err := s.claims.Claim(ctx, msg)
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
	default:
		return fmt.Errorf("unknown queue claim decision %d", decision)
	}

	reservation, err := s.chain.Reserve(ctx, reservationRequestForMessage(msg))
	if err != nil {
		return err
	}

	// A completed chain commit can be observed after a crash between Commit
	// and ClaimStore.Complete. Do not invoke the provider effect a second time.
	if !reservation.Committed {
		commit, effectErr := s.effect(ctx, msg, reservation)
		if effectErr != nil {
			return effectErr
		}
		if err := bindReservation(&commit, reservation); err != nil {
			return err
		}
		if err := s.chain.Commit(ctx, commit); err != nil {
			return err
		}
	}

	if err := s.claims.Complete(ctx, msg); err != nil {
		return fmt.Errorf("complete ordered stream claim: %w", err)
	}
	return ack()
}

func reservationRequestForMessage(msg Message) EGSSequenceReservationRequest {
	return EGSSequenceReservationRequest{
		EGSSequenceIdentity: EGSSequenceIdentity{
			EGSSequenceID: msg.EGSSequenceID,
			TenantID:      msg.TenantID,
			BranchID:      msg.BranchID,
			VATNumber:     msg.VATNumber,
			EGSID:         msg.EGSID,
		},
		ReservationKey:   msg.RequestKey(),
		ReservationToken: msg.EventID,
		DocumentUUID:     msg.InvoiceUUID,
		OutboxEventID:    msg.EventID,
	}
}

func bindReservation(commit *EGSSequenceCommitRequest, reservation EGSSequenceReservation) error {
	if commit == nil {
		return errors.New("ordered stream chain commit is required")
	}
	if commit.Reservation.ReservationKey == "" {
		commit.Reservation = reservation
		return nil
	}
	if !sameReservationIdentity(commit.Reservation, reservation) {
		return fmt.Errorf("%w: commit reservation does not match delivery", ErrEGSSequenceOwnership)
	}
	return nil
}

func sameReservationIdentity(left, right EGSSequenceReservation) bool {
	return left.EGSSequenceID == right.EGSSequenceID &&
		left.TenantID == right.TenantID &&
		left.BranchID == right.BranchID &&
		left.VATNumber == right.VATNumber &&
		left.EGSID == right.EGSID &&
		left.ReservationKey == right.ReservationKey &&
		left.ReservationToken == right.ReservationToken &&
		left.DocumentUUID == right.DocumentUUID &&
		left.OutboxEventID == right.OutboxEventID &&
		left.ICV == right.ICV &&
		equalStringPointer(left.PIH, right.PIH)
}

func equalStringPointer(left, right *string) bool {
	switch {
	case left == nil && right == nil:
		return true
	case left == nil || right == nil:
		return false
	default:
		return *left == *right
	}
}

func validateEGSReservationRequest(request EGSSequenceReservationRequest) error {
	if strings.TrimSpace(request.EGSSequenceID) == "" ||
		request.TenantID == 0 ||
		request.BranchID == 0 ||
		strings.TrimSpace(request.VATNumber) == "" ||
		strings.TrimSpace(request.EGSID) == "" {
		return errors.New("EGS sequence ID, tenant ID, branch ID, VAT number, and EGS ID are required")
	}
	if strings.TrimSpace(request.ReservationKey) == "" ||
		strings.TrimSpace(request.ReservationToken) == "" ||
		strings.TrimSpace(request.DocumentUUID) == "" ||
		strings.TrimSpace(request.OutboxEventID) == "" {
		return errors.New("reservation key, token, document UUID, and outbox event ID are required")
	}
	return nil
}

func validateEGSReservation(reservation EGSSequenceReservation) error {
	if err := validateEGSReservationRequest(EGSSequenceReservationRequest{
		EGSSequenceIdentity: EGSSequenceIdentity{
			EGSSequenceID: reservation.EGSSequenceID,
			TenantID:      reservation.TenantID,
			BranchID:      reservation.BranchID,
			VATNumber:     reservation.VATNumber,
			EGSID:         reservation.EGSID,
		},
		ReservationKey:   reservation.ReservationKey,
		ReservationToken: reservation.ReservationToken,
		DocumentUUID:     reservation.DocumentUUID,
		OutboxEventID:    reservation.OutboxEventID,
	}); err != nil {
		return err
	}
	if reservation.ICV == 0 && !reservation.Committed {
		return errors.New("EGS sequence reservation ICV is required")
	}
	return nil
}

func validateEGSCommitRequest(request EGSSequenceCommitRequest) error {
	if err := validateEGSReservation(request.Reservation); err != nil {
		return err
	}
	if strings.TrimSpace(request.DocumentType) == "" {
		return errors.New("document type is required")
	}
	if request.SourceID == 0 {
		return errors.New("source ID is required")
	}
	if len(request.Snapshot) == 0 {
		return errors.New("immutable document snapshot is required")
	}
	if strings.TrimSpace(request.DocumentHash) == "" {
		return errors.New("document hash is required")
	}
	if request.State != "" {
		switch request.State {
		case EGSDocumentQueued, EGSDocumentSubmitted, EGSDocumentAccepted,
			EGSDocumentAcceptedWithWarning, EGSDocumentRejected,
			EGSDocumentAmbiguous, EGSDocumentTerminal:
		default:
			return fmt.Errorf("invalid EGS document state %q", request.State)
		}
	}
	return nil
}

type memoryEGSSequence struct {
	lastICV      uint64
	previousHash *string
	reservation  *EGSSequenceReservation
}

// MemoryEGSSequenceStore is a deterministic implementation of the backend
// chain contract for tests and local harnesses.
type MemoryEGSSequenceStore struct {
	mu             sync.Mutex
	sequences      map[string]*memoryEGSSequence
	documents      map[string]EGSSequenceDocument
	order          map[string][]string
	committedByKey map[string]EGSSequenceReservation
	LeaseDuration  time.Duration
	Now            func() time.Time
}

// NewMemoryEGSSequenceStore returns an empty ordered chain store.
func NewMemoryEGSSequenceStore() *MemoryEGSSequenceStore {
	return &MemoryEGSSequenceStore{
		sequences:      make(map[string]*memoryEGSSequence),
		documents:      make(map[string]EGSSequenceDocument),
		order:          make(map[string][]string),
		committedByKey: make(map[string]EGSSequenceReservation),
		LeaseDuration:  defaultEGSSequenceLease,
	}
}

func (s *MemoryEGSSequenceStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *MemoryEGSSequenceStore) ensureMaps() {
	if s.sequences == nil {
		s.sequences = make(map[string]*memoryEGSSequence)
	}
	if s.documents == nil {
		s.documents = make(map[string]EGSSequenceDocument)
	}
	if s.order == nil {
		s.order = make(map[string][]string)
	}
	if s.committedByKey == nil {
		s.committedByKey = make(map[string]EGSSequenceReservation)
	}
	if s.LeaseDuration <= 0 {
		s.LeaseDuration = defaultEGSSequenceLease
	}
}

func (s *MemoryEGSSequenceStore) Reserve(_ context.Context, request EGSSequenceReservationRequest) (EGSSequenceReservation, error) {
	if err := validateEGSReservationRequest(request); err != nil {
		return EGSSequenceReservation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMaps()

	partition := request.EGSSequenceIdentity
	partitionKey := fmt.Sprintf("%d\x00%s", partition.TenantID, partition.EGSSequenceID)
	requestKey := partitionKey + "\x00" + request.ReservationKey
	if committed, ok := s.committedByKey[requestKey]; ok {
		if committed.DocumentUUID != request.DocumentUUID || committed.OutboxEventID != request.OutboxEventID {
			return EGSSequenceReservation{}, fmt.Errorf("%w: %s", ErrEGSSequenceReservationConflict, request.ReservationKey)
		}
		committed.Committed = true
		return committed, nil
	}

	state := s.sequences[partitionKey]
	if state == nil {
		state = &memoryEGSSequence{}
		s.sequences[partitionKey] = state
	}
	now := s.now()
	if state.reservation != nil {
		active := *state.reservation
		if active.ReservationKey == request.ReservationKey {
			if active.DocumentUUID != request.DocumentUUID || active.OutboxEventID != request.OutboxEventID {
				return EGSSequenceReservation{}, fmt.Errorf("%w: %s", ErrEGSSequenceReservationConflict, request.ReservationKey)
			}
			return active, nil
		}
		if active.LeaseExpiresAt.After(now) {
			return EGSSequenceReservation{}, fmt.Errorf("%w: %s", ErrEGSSequenceBusy, request.EGSSequenceID)
		}
		state.reservation = nil
	}

	leaseDuration := request.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = s.LeaseDuration
	}
	reservation := EGSSequenceReservation{
		EGSSequenceIdentity: request.EGSSequenceIdentity,
		ReservationKey:      request.ReservationKey,
		ReservationToken:    request.ReservationToken,
		DocumentUUID:        request.DocumentUUID,
		OutboxEventID:       request.OutboxEventID,
		ICV:                 state.lastICV + 1,
		PIH:                 cloneStringPointer(state.previousHash),
		LeaseExpiresAt:      now.Add(leaseDuration),
	}
	state.reservation = &reservation
	return reservation, nil
}

func (s *MemoryEGSSequenceStore) Commit(_ context.Context, request EGSSequenceCommitRequest) error {
	if err := validateEGSCommitRequest(request); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMaps()

	partitionKey := fmt.Sprintf("%d\x00%s", request.Reservation.TenantID, request.Reservation.EGSSequenceID)
	requestKey := partitionKey + "\x00" + request.Reservation.ReservationKey
	documentKey := partitionKey + "\x00" + request.Reservation.DocumentUUID
	if committed, ok := s.committedByKey[requestKey]; ok {
		if !sameReservationIdentity(committed, request.Reservation) {
			return fmt.Errorf("%w: %s", ErrEGSSequenceOwnership, request.Reservation.EGSSequenceID)
		}
		existing, exists := s.documents[documentKey]
		if !exists ||
			existing.DocumentType != request.DocumentType ||
			existing.SourceID != request.SourceID ||
			existing.OutboxEventID != request.Reservation.OutboxEventID ||
			existing.ICV != request.Reservation.ICV ||
			!equalStringPointer(existing.PIH, request.Reservation.PIH) ||
			existing.DocumentHash != request.DocumentHash ||
			string(existing.Snapshot) != string(request.Snapshot) {
			return fmt.Errorf("%w: immutable document %s changed", ErrEGSSequenceReservationConflict, request.Reservation.DocumentUUID)
		}
		return nil
	}

	if existing, ok := s.documents[documentKey]; ok {
		if existing.DocumentType != request.DocumentType ||
			existing.SourceID != request.SourceID ||
			existing.OutboxEventID != request.Reservation.OutboxEventID ||
			existing.ICV != request.Reservation.ICV ||
			!equalStringPointer(existing.PIH, request.Reservation.PIH) ||
			existing.DocumentHash != request.DocumentHash ||
			string(existing.Snapshot) != string(request.Snapshot) {
			return fmt.Errorf("%w: immutable document %s changed", ErrEGSSequenceReservationConflict, request.Reservation.DocumentUUID)
		}
		s.committedByKey[requestKey] = request.Reservation
		return nil
	}

	state := s.sequences[partitionKey]
	if state == nil || state.reservation == nil ||
		!sameReservationIdentity(*state.reservation, request.Reservation) {
		return fmt.Errorf("%w: %s", ErrEGSSequenceOwnership, request.Reservation.EGSSequenceID)
	}
	if request.State == "" {
		request.State = EGSDocumentQueued
	}
	document := EGSSequenceDocument{
		EGSSequenceID: request.Reservation.EGSSequenceID,
		TenantID:      request.Reservation.TenantID,
		BranchID:      request.Reservation.BranchID,
		VATNumber:     request.Reservation.VATNumber,
		EGSID:         request.Reservation.EGSID,
		DocumentUUID:  request.Reservation.DocumentUUID,
		DocumentType:  request.DocumentType,
		SourceID:      request.SourceID,
		OutboxEventID: request.Reservation.OutboxEventID,
		ICV:           request.Reservation.ICV,
		PIH:           cloneStringPointer(request.Reservation.PIH),
		DocumentHash:  request.DocumentHash,
		Snapshot:      append([]byte(nil), request.Snapshot...),
		State:         request.State,
	}
	s.documents[documentKey] = document
	s.order[partitionKey] = append(s.order[partitionKey], documentKey)
	state.lastICV = request.Reservation.ICV
	state.previousHash = cloneStringPointer(&request.DocumentHash)
	state.reservation = nil
	s.committedByKey[requestKey] = request.Reservation
	return nil
}

func (s *MemoryEGSSequenceStore) Release(_ context.Context, reservation EGSSequenceReservation) error {
	if err := validateEGSReservation(reservation); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMaps()
	partitionKey := fmt.Sprintf("%d\x00%s", reservation.TenantID, reservation.EGSSequenceID)
	state := s.sequences[partitionKey]
	if state == nil || state.reservation == nil ||
		!sameReservationIdentity(*state.reservation, reservation) {
		return fmt.Errorf("%w: %s", ErrEGSSequenceOwnership, reservation.EGSSequenceID)
	}
	state.reservation = nil
	return nil
}

// Documents returns committed documents in ICV order for one partition.
func (s *MemoryEGSSequenceStore) Documents(partition string) []EGSSequenceDocument {
	s.mu.Lock()
	defer s.mu.Unlock()
	var documents []EGSSequenceDocument
	for _, key := range s.order[partition] {
		document := s.documents[key]
		document.Snapshot = append([]byte(nil), document.Snapshot...)
		document.PIH = cloneStringPointer(document.PIH)
		documents = append(documents, document)
	}
	return documents
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
