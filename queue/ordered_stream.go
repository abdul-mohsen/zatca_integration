package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// Prepared is a bill that passed verification. Build turns it into a signed invoice under a booking.
type Prepared interface {
	Build(ctx context.Context, booking Booking) (SignedInvoice, error)
}

// Pipeline is the fiscal work of one document type. Prepare verifies a document and loads what Build needs, before any number
// is booked. Submit sends a committed invoice to ZATCA. It returns an error only for a technical failure. A rejection by ZATCA
// is an Outcome. An ambiguous answer is an Outcome too, returned with ErrOutcomeAmbiguous.
type Pipeline interface {
	Prepare(ctx context.Context, msg Message) (Prepared, error)
	Submit(ctx context.Context, invoice SignedInvoice) (Outcome, error)
}

// ErrInvoiceRejected means ZATCA rejected an invoice that passed verification. The rejection is recorded and the request ends.
// Verification let it through, so it is a defect to fix on this side. It is reported, and it is never retried.
var ErrInvoiceRejected = errors.New("ZATCA rejected a verified invoice")

// ErrOutcomeAmbiguous means ZATCA's answer does not say whether the invoice is accepted, for example because ZATCA already holds
// the invoice. The answer is recorded and the request ends, so the invoice is not sent again. It is reported for reconciliation.
var ErrOutcomeAmbiguous = errors.New("ZATCA's outcome is ambiguous")

// OrderedStream runs the deliveries of a tenant's branches through the chain protocol. Deliveries of one tenant and EGS run one
// at a time, in order.
//
// The delivery claims its request first. A bill that already has a committed invoice skips to submission, and an invoice whose
// answer is recorded is never sent again. Otherwise the bill is verified before any number is booked, built and signed under its
// booking, and committed together with the chain update.
//
// Locks are released on every path. A failure before the commit releases the booking, and nothing is committed. A failure that is
// not a rejected input releases the claim too, so the redelivery runs at once. A committed invoice is never rebuilt: the
// redelivery submits the stored XML again, unless ZATCA's answer is already recorded.
type OrderedStream struct {
	claims   ClaimStore
	chain    Chain
	pipeline Pipeline

	mu        sync.Mutex
	partition map[string]*sync.Mutex
}

// NewOrderedStream creates the protocol for one tenant database.
func NewOrderedStream(claims ClaimStore, chain Chain, pipeline Pipeline) *OrderedStream {
	return &OrderedStream{
		claims:    claims,
		chain:     chain,
		pipeline:  pipeline,
		partition: make(map[string]*sync.Mutex),
	}
}

func (s *OrderedStream) lockFor(partition string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.partition[partition]
	if !ok {
		lock = &sync.Mutex{}
		s.partition[partition] = lock
	}
	return lock
}

// HandleMessage processes one decoded delivery and acknowledges it once its outcome is final.
func (s *OrderedStream) HandleMessage(ctx context.Context, msg Message, ack func() error) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	if ack == nil {
		return errors.New("ordered stream ACK function is required")
	}
	// A redelivery keeps the same event_id, so the database owner must use a
	// fresh fence for each claim attempt. Direct ClaimStore callers may omit
	// it and retain the event_id fallback for compatibility.
	msg.ClaimToken = uuid.NewString()
	lock := s.lockFor(msg.PartitionKey())
	lock.Lock()
	defer lock.Unlock()

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
		return s.process(ctx, msg, ack)
	default:
		return fmt.Errorf("unknown queue claim decision %d", decision)
	}
}

// FinalizePermanentFailure records a bounded local failure and acknowledges
// the delivery. It is intentionally separate from provider outcomes: no
// provider result is fabricated when the daemon exhausts its delivery budget.
func (s *OrderedStream) FinalizePermanentFailure(ctx context.Context, msg Message, cause error, ack func() error) error {
	if cause == nil {
		return errors.New("permanent failure cause is required")
	}
	if ack == nil {
		return errors.New("ordered stream ACK function is required")
	}
	invoice, err := s.chain.Stored(ctx, msg)
	if err != nil {
		return errors.Join(cause, err)
	}
	if invoice != nil && invoice.Outcome == "" {
		if err := s.chain.Record(ctx, *invoice, Outcome{
			State:        OutcomeTerminalFailure,
			ProviderCode: "DAEMON_RETRY_EXHAUSTED",
			Message:      cause.Error(),
			Evidence:     EvidenceLocal,
		}); err != nil {
			return errors.Join(cause, err)
		}
	}
	if err := s.claims.Complete(ctx, msg); err != nil {
		return errors.Join(cause, err)
	}
	if err := ack(); err != nil {
		return errors.Join(cause, err)
	}
	return nil
}

// process runs a request whose claim this delivery holds.
func (s *OrderedStream) process(ctx context.Context, msg Message, ack func() error) error {
	invoice, err := s.invoiceFor(ctx, msg)
	if err != nil {
		return s.failed(ctx, msg, err, ack)
	}
	if invoice.Outcome != "" {
		// ZATCA's answer is already recorded, so the invoice is final and nothing is sent again.
		return s.finish(ctx, msg, ack, nil)
	}
	outcome, err := s.pipeline.Submit(ctx, invoice)
	if err != nil && !errors.Is(err, ErrOutcomeAmbiguous) {
		return s.failed(ctx, msg, err, ack)
	}
	if err := s.chain.Record(ctx, invoice, outcome); err != nil {
		return s.failed(ctx, msg, err, ack)
	}
	if outcome.State == OutcomeRejected {
		err = fmt.Errorf("%w: bill %d: %s", ErrInvoiceRejected, msg.ID, outcome.Message)
	}
	return s.finish(ctx, msg, ack, err)
}

// finish completes the claim and acknowledges the delivery. The result, if any, is returned after that.
func (s *OrderedStream) finish(ctx context.Context, msg Message, ack func() error, result error) error {
	if err := s.claims.Complete(ctx, msg); err != nil {
		return s.failed(ctx, msg, fmt.Errorf("complete ordered stream claim: %w", err), ack)
	}
	if err := ack(); err != nil {
		return errors.Join(result, fmt.Errorf("ACK failed: %w", err))
	}
	return result
}

// invoiceFor returns the bill's committed invoice. It builds and commits one only when the bill has none yet.
func (s *OrderedStream) invoiceFor(ctx context.Context, msg Message) (SignedInvoice, error) {
	stored, err := s.chain.Stored(ctx, msg)
	if err != nil {
		return SignedInvoice{}, err
	}
	if stored != nil {
		return checkStored(*stored, msg)
	}

	prepared, err := s.pipeline.Prepare(ctx, msg)
	if err != nil {
		return SignedInvoice{}, err
	}
	booking, err := s.chain.Book(ctx, msg)
	if err != nil {
		return SignedInvoice{}, err
	}
	invoice, err := prepared.Build(ctx, booking)
	if err != nil {
		return SignedInvoice{}, s.release(ctx, booking, err)
	}
	if err := checkBuilt(invoice, msg, booking); err != nil {
		return SignedInvoice{}, s.release(ctx, booking, err)
	}
	if err := s.chain.Commit(ctx, booking, invoice); err != nil {
		return SignedInvoice{}, s.release(ctx, booking, err)
	}
	return invoice, nil
}

// checkStored refuses a stored invoice that belongs to another UUID or branch. The invoice was committed earlier, so a mismatch
// is a technical failure for an operator to look at. It is not an input error.
func checkStored(invoice SignedInvoice, msg Message) (SignedInvoice, error) {
	if err := invoice.Validate(); err != nil {
		return SignedInvoice{}, fmt.Errorf("stored bill %d is invalid: %w", msg.ID, err)
	}
	if invoice.SourceID != msg.ID {
		return SignedInvoice{}, fmt.Errorf("stored invoice source %d does not match bill %d", invoice.SourceID, msg.ID)
	}
	if invoice.UUID != msg.InvoiceUUID || invoice.BranchID != msg.BranchID {
		return SignedInvoice{}, fmt.Errorf("bill %d is stored as invoice %s of branch %d, not %s of branch %d",
			msg.ID, invoice.UUID, invoice.BranchID, msg.InvoiceUUID, msg.BranchID)
	}
	return invoice, nil
}

// checkBuilt enforces that a pipeline cannot replace the backend's invoice
// identity or the chain booking before the signed invoice is persisted.
func checkBuilt(invoice SignedInvoice, msg Message, booking Booking) error {
	if err := invoice.Validate(); err != nil {
		return fmt.Errorf("built bill %d is invalid: %w", msg.ID, err)
	}
	switch {
	case invoice.SourceID != msg.ID:
		return fmt.Errorf("built invoice source %d does not match bill %d", invoice.SourceID, msg.ID)
	case invoice.BranchID != msg.BranchID:
		return fmt.Errorf("built invoice branch %d does not match branch %d", invoice.BranchID, msg.BranchID)
	case invoice.UUID != msg.InvoiceUUID || invoice.UUID != booking.UUID:
		return fmt.Errorf("built invoice UUID %s does not match the backend UUID %s", invoice.UUID, msg.InvoiceUUID)
	case invoice.ICV != booking.ICV:
		return fmt.Errorf("built invoice ICV %d does not match booked ICV %d", invoice.ICV, booking.ICV)
	case invoice.PIH != booking.PIH:
		return fmt.Errorf("built invoice PIH does not match the booking")
	}
	return nil
}

// release drops a booking after a failure before the commit. The cause is kept, and a release error is joined to it.
func (s *OrderedStream) release(ctx context.Context, booking Booking, cause error) error {
	if err := s.chain.Release(ctx, booking); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// failed handles a failure of a claimed request. A rejected input ends the request: it is completed and acknowledged, and the
// error is returned for the report. Any other failure is technical. The claim is released, so the redelivery can run at once,
// and the delivery stays unacknowledged.
func (s *OrderedStream) failed(ctx context.Context, msg Message, cause error, ack func() error) error {
	if !errors.Is(cause, ErrRejectedInput) {
		if err := s.claims.Release(ctx, msg); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	if err := s.claims.Complete(ctx, msg); err != nil {
		return errors.Join(cause, err)
	}
	if err := ack(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
