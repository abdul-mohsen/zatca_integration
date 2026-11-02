package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Prepared is a bill that passed verification. Build turns it into a signed invoice under a booking.
type Prepared interface {
	Build(ctx context.Context, booking Booking) (SignedInvoice, error)
}

// Pipeline is the fiscal work of one document type. Prepare verifies a document and loads what Build needs, before any
// number is booked. Submit sends a committed invoice to ZATCA. It returns an error only for a technical failure. A rejection
// by ZATCA is an Outcome.
type Pipeline interface {
	Prepare(ctx context.Context, msg Message) (Prepared, error)
	Submit(ctx context.Context, invoice SignedInvoice) (Outcome, error)
}

// OrderedStream runs the deliveries of a tenant's branches through the chain protocol. Deliveries of one tenant and EGS run
// one at a time, in order.
//
// A delivery is claimed first. A bill that already has a committed invoice skips to submission, so a redelivery never signs
// twice. Otherwise the bill is verified before any number is booked, then built and signed under its booking, then committed
// together with the chain update. A failure before the commit releases the booking, and nothing is committed. A failure after
// it leaves the delivery unacknowledged: the next delivery submits the stored XML again and never rebuilds it. The delivery is
// acknowledged once ZATCA's outcome is recorded and the claim is complete.
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
	default:
		return fmt.Errorf("unknown queue claim decision %d", decision)
	}

	invoice, err := s.invoiceFor(ctx, msg)
	if err != nil {
		return s.endOnFailure(ctx, msg, err, ack)
	}
	outcome, err := s.pipeline.Submit(ctx, invoice)
	if err != nil {
		return err
	}
	if err := s.chain.Record(ctx, invoice, outcome); err != nil {
		return err
	}
	if err := s.claims.Complete(ctx, msg); err != nil {
		return fmt.Errorf("complete ordered stream claim: %w", err)
	}
	return ack()
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
	if err := s.chain.Commit(ctx, booking, invoice); err != nil {
		return SignedInvoice{}, s.release(ctx, booking, err)
	}
	return invoice, nil
}

// checkStored refuses a committed invoice that belongs to another UUID or branch. That cannot happen unless the data was
// changed outside the daemon.
func checkStored(invoice SignedInvoice, msg Message) (SignedInvoice, error) {
	if invoice.UUID != msg.InvoiceUUID || invoice.BranchID != msg.BranchID {
		return SignedInvoice{}, fmt.Errorf("%w: bill %d is invoiced as %s, not %s",
			ErrRejectedInput, msg.ID, invoice.UUID, msg.InvoiceUUID)
	}
	return invoice, nil
}

// release drops a booking after a failure before the commit. The cause is kept, and a release error is joined to it.
func (s *OrderedStream) release(ctx context.Context, booking Booking, cause error) error {
	if err := s.chain.Release(ctx, booking); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// endOnFailure separates the two kinds of failure. A rejected input ends the request: it is completed and acknowledged, and
// the error is still returned so it can be reported. Any other failure is technical, so the delivery stays unacknowledged.
func (s *OrderedStream) endOnFailure(ctx context.Context, msg Message, cause error, ack func() error) error {
	if !errors.Is(cause, ErrRejectedInput) {
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
