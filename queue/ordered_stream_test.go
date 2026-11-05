package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
)

// calls records the steps of the protocol in the order the fakes saw them.
type calls []string

func (c *calls) add(step string) {
	*c = append(*c, step)
}

// fakeClaims answers the claim with a fixed decision.
type fakeClaims struct {
	calls       *calls
	decision    ClaimDecision
	completeErr error
}

func (f *fakeClaims) Claim(context.Context, Message) (ClaimDecision, error) {
	f.calls.add("claim")
	return f.decision, nil
}

func (f *fakeClaims) Complete(context.Context, Message) error {
	f.calls.add("complete")
	return f.completeErr
}

func (f *fakeClaims) Release(context.Context, Message) error {
	f.calls.add("unclaim")
	return nil
}

// fakeChain keeps one committed invoice, as the database would, and records ZATCA's answer on it.
type fakeChain struct {
	calls     *calls
	stored    *SignedInvoice
	bookErr   error
	commitErr error
	recordErr error
	recorded  []Outcome
}

func (c *fakeChain) Stored(context.Context, Message) (*SignedInvoice, error) {
	c.calls.add("stored")
	if c.stored == nil {
		return nil, nil
	}
	copied := *c.stored
	return &copied, nil
}

func (c *fakeChain) Book(_ context.Context, msg Message) (Booking, error) {
	c.calls.add("book")
	if c.bookErr != nil {
		return Booking{}, c.bookErr
	}
	return Booking{
		BranchID:   msg.BranchID,
		RequestKey: msg.RequestKey(),
		EventID:    msg.EventID,
		UUID:       msg.InvoiceUUID,
		ICV:        1,
		PIH:        GenesisPIH,
	}, nil
}

func (c *fakeChain) Release(context.Context, Booking) error {
	c.calls.add("release")
	return nil
}

func (c *fakeChain) Commit(_ context.Context, _ Booking, invoice SignedInvoice) error {
	c.calls.add("commit")
	if c.commitErr != nil {
		return c.commitErr
	}
	c.stored = &invoice
	return nil
}

func (c *fakeChain) Record(_ context.Context, _ SignedInvoice, outcome Outcome) error {
	c.calls.add("record")
	if c.recordErr != nil {
		return c.recordErr
	}
	c.recorded = append(c.recorded, outcome)
	if c.stored != nil {
		c.stored.Outcome = outcome.State
	}
	return nil
}

// fakePipeline verifies, builds, and submits with fixed results.
type fakePipeline struct {
	calls      *calls
	sourceID   int64
	prepareErr error
	buildErr   error
	submitErr  error
	outcome    Outcome
	outcomeErr error
	built      []Booking
	submitted  []SignedInvoice
}

func (p *fakePipeline) Prepare(context.Context, Message) (Prepared, error) {
	p.calls.add("prepare")
	if p.prepareErr != nil {
		return nil, p.prepareErr
	}
	return &fakePrepared{pipeline: p}, nil
}

func (p *fakePipeline) Submit(_ context.Context, invoice SignedInvoice) (Outcome, error) {
	p.calls.add("submit")
	p.submitted = append(p.submitted, invoice)
	if p.submitErr != nil {
		return Outcome{}, p.submitErr
	}
	return p.outcome, p.outcomeErr
}

type fakePrepared struct {
	pipeline *fakePipeline
}

func (f *fakePrepared) Build(_ context.Context, booking Booking) (SignedInvoice, error) {
	f.pipeline.calls.add("build")
	f.pipeline.built = append(f.pipeline.built, booking)
	if f.pipeline.buildErr != nil {
		return SignedInvoice{}, f.pipeline.buildErr
	}
	sourceID := f.pipeline.sourceID
	if sourceID == 0 {
		sourceID = 42
	}
	return SignedInvoice{
		SourceID:  sourceID,
		BranchID:  booking.BranchID,
		UUID:      booking.UUID,
		ICV:       booking.ICV,
		PIH:       booking.PIH,
		Hash:      "hash-of-bill-42",
		Kind:      InvoiceStandard,
		SignedXML: "<Invoice/>",
		QR:        "qr",
	}, nil
}

type harness struct {
	calls    calls
	claims   *fakeClaims
	chain    *fakeChain
	pipeline *fakePipeline
	acks     int
	stream   *OrderedStream
	msg      Message
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{}
	h.claims = &fakeClaims{calls: &h.calls, decision: ClaimProcess}
	h.chain = &fakeChain{calls: &h.calls}
	h.pipeline = &fakePipeline{calls: &h.calls, outcome: Outcome{State: OutcomeAccepted}}
	h.stream = NewOrderedStream(h.claims, h.chain, h.pipeline)
	msg, err := DecodeEnvelope(readFixture(t, "bill_envelope.json"))
	if err != nil {
		t.Fatalf("decode the backend envelope: %v", err)
	}
	h.msg = msg
	return h
}

func (h *harness) deliver() error {
	return h.stream.HandleMessage(context.Background(), h.msg, func() error {
		h.acks++
		return nil
	})
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func expectSteps(t *testing.T, got calls, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, calls(want)) {
		t.Fatalf("steps = %v; want %v", got, want)
	}
}

func TestBillIsVerifiedBookedBuiltCommittedSubmittedAndCompleted(t *testing.T) {
	h := newHarness(t)
	if err := h.deliver(); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "prepare", "book", "build", "commit", "submit", "record", "complete")
	if h.acks != 1 {
		t.Fatalf("acks = %d; want 1", h.acks)
	}
	if got := h.pipeline.built[0].UUID; got != h.msg.InvoiceUUID {
		t.Fatalf("built with UUID %s; want the envelope's %s", got, h.msg.InvoiceUUID)
	}
}

func TestRejectedInputBooksNothingAndEndsTheRequest(t *testing.T) {
	h := newHarness(t)
	h.pipeline.prepareErr = fmt.Errorf("%w: the bill has no lines", ErrRejectedInput)
	err := h.deliver()
	if !errors.Is(err, ErrRejectedInput) {
		t.Fatalf("deliver = %v; want ErrRejectedInput", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "prepare", "complete")
	if h.acks != 1 {
		t.Fatalf("acks = %d; want 1", h.acks)
	}
}

func TestBuildFailureReleasesTheBookingAndTheClaim(t *testing.T) {
	h := newHarness(t)
	h.pipeline.buildErr = errors.New("signer unavailable")
	if err := h.deliver(); err == nil || errors.Is(err, ErrRejectedInput) {
		t.Fatalf("deliver = %v; want a technical error", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "prepare", "book", "build", "release", "unclaim")
	if h.acks != 0 || h.chain.stored != nil {
		t.Fatalf("acks = %d, stored = %v; want no ACK and nothing committed", h.acks, h.chain.stored)
	}
}

func TestCommitFailureReleasesTheBookingAndTheClaim(t *testing.T) {
	h := newHarness(t)
	h.chain.commitErr = errors.New("connection lost")
	if err := h.deliver(); err == nil {
		t.Fatal("deliver succeeded; want the commit error")
	}
	expectSteps(t, h.calls, "claim", "stored", "prepare", "book", "build", "commit", "release", "unclaim")
	if h.acks != 0 || len(h.pipeline.submitted) != 0 {
		t.Fatalf("acks = %d, submitted = %d; want no ACK and no submission", h.acks, len(h.pipeline.submitted))
	}
}

func TestBusyChainReleasesTheClaimAndLeavesTheDelivery(t *testing.T) {
	h := newHarness(t)
	h.chain.bookErr = ErrChainBusy
	if err := h.deliver(); !errors.Is(err, ErrChainBusy) {
		t.Fatalf("deliver = %v; want ErrChainBusy", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "prepare", "book", "unclaim")
	if h.acks != 0 {
		t.Fatalf("acks = %d; want 0", h.acks)
	}
}

func TestRedeliveryAfterASubmitFailureSubmitsTheStoredXMLAgain(t *testing.T) {
	h := newHarness(t)
	h.pipeline.submitErr = errors.New("ZATCA unreachable")
	if err := h.deliver(); err == nil {
		t.Fatal("first delivery succeeded; want the submit error")
	}
	if h.chain.stored == nil {
		t.Fatal("the invoice was not committed before the submit failed")
	}

	h.pipeline.submitErr = nil
	h.calls = nil
	if err := h.deliver(); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "submit", "record", "complete")
	if len(h.pipeline.built) != 1 {
		t.Fatalf("built %d times; want once, the stored XML is never rebuilt", len(h.pipeline.built))
	}
	first, second := h.pipeline.submitted[0], h.pipeline.submitted[1]
	if first.SignedXML != second.SignedXML || first.Hash != second.Hash || first.ICV != second.ICV {
		t.Fatalf("the redelivery submitted %+v; want the stored invoice %+v", second, first)
	}
	if h.acks != 1 {
		t.Fatalf("acks = %d; want 1", h.acks)
	}
}

func TestRecordFailureAfterAnAnswerKeepsTheDeliveryAndReleasesTheClaim(t *testing.T) {
	h := newHarness(t)
	h.chain.recordErr = errors.New("connection lost")
	if err := h.deliver(); err == nil {
		t.Fatal("deliver succeeded; want the record error")
	}
	expectSteps(t, h.calls, "claim", "stored", "prepare", "book", "build", "commit", "submit", "record", "unclaim")
	if h.acks != 0 {
		t.Fatalf("acks = %d; want 0", h.acks)
	}
}

// If the provider accepted an invoice but the process failed before recording
// the answer, a replay can receive 409. It must use the stored XML, record the
// ambiguity, and acknowledge only after that record succeeds.
func TestAcceptedThenRecordFailureReplaysStoredXMLAndRecordsAmbiguous(t *testing.T) {
	h := newHarness(t)
	h.chain.recordErr = errors.New("crash after provider acceptance")
	if err := h.deliver(); err == nil {
		t.Fatal("first delivery succeeded; want the record failure")
	}
	if h.chain.stored == nil || len(h.pipeline.submitted) != 1 {
		t.Fatalf("stored = %v, submitted = %d; want a committed invoice and one provider call", h.chain.stored, len(h.pipeline.submitted))
	}
	first := h.pipeline.submitted[0]

	h.chain.recordErr = nil
	h.pipeline.outcome = Outcome{State: OutcomeAmbiguous, ProviderCode: "409", Message: "already reported"}
	h.pipeline.outcomeErr = fmt.Errorf("%w: provider already holds the invoice", ErrOutcomeAmbiguous)
	h.calls = nil
	if err := h.deliver(); !errors.Is(err, ErrOutcomeAmbiguous) {
		t.Fatalf("redelivery = %v; want ErrOutcomeAmbiguous", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "submit", "record", "complete")
	if len(h.pipeline.built) != 1 {
		t.Fatalf("built %d times; want once", len(h.pipeline.built))
	}
	if len(h.pipeline.submitted) != 2 {
		t.Fatalf("submitted %d times; want the original and one replay", len(h.pipeline.submitted))
	}
	second := h.pipeline.submitted[1]
	if second.SignedXML != first.SignedXML || second.UUID != first.UUID || second.ICV != first.ICV {
		t.Fatalf("replay = %+v; want the exact stored invoice %+v", second, first)
	}
	if len(h.chain.recorded) != 1 || h.chain.recorded[0].State != OutcomeAmbiguous {
		t.Fatalf("recorded = %+v; want one ambiguous outcome", h.chain.recorded)
	}
	if h.acks != 1 {
		t.Fatalf("acks = %d; want 1 after the ambiguous outcome was recorded", h.acks)
	}
}

// Once ZATCA's answer is recorded, the invoice is final. A redelivery that has to finish the request must not send it again.
func TestRecordedAnswerIsNeverSubmittedAgain(t *testing.T) {
	h := newHarness(t)
	h.claims.completeErr = errors.New("connection lost")
	if err := h.deliver(); err == nil {
		t.Fatal("first delivery succeeded; want the complete error")
	}
	h.claims.completeErr = nil
	h.calls = nil
	if err := h.deliver(); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "complete")
	if len(h.pipeline.submitted) != 1 {
		t.Fatalf("submitted %d times; want once", len(h.pipeline.submitted))
	}
	if h.acks != 1 {
		t.Fatalf("acks = %d; want 1", h.acks)
	}
}

func TestRejectedOutcomeIsRecordedAndReportedAfterTheAck(t *testing.T) {
	h := newHarness(t)
	h.pipeline.outcome = Outcome{State: OutcomeRejected, ProviderCode: "400", Message: "NOT_VALID"}
	if err := h.deliver(); !errors.Is(err, ErrInvoiceRejected) {
		t.Fatalf("deliver = %v; want ErrInvoiceRejected", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "prepare", "book", "build", "commit", "submit", "record", "complete")
	if len(h.chain.recorded) != 1 || h.chain.recorded[0].State != OutcomeRejected {
		t.Fatalf("recorded = %+v; want one rejection", h.chain.recorded)
	}
	if h.acks != 1 {
		t.Fatalf("acks = %d; want 1", h.acks)
	}
}

// An ambiguous answer is recorded as it is, never as a rejection. The claim completes, and the redelivery never sends the
// invoice again. The error is reported for reconciliation.
func TestAmbiguousAnswerCompletesTheClaimAndIsNeverSentAgain(t *testing.T) {
	h := newHarness(t)
	h.pipeline.outcome = Outcome{State: OutcomeAmbiguous, ProviderCode: "409", Message: "already reported"}
	h.pipeline.outcomeErr = fmt.Errorf("%w: bill 42", ErrOutcomeAmbiguous)
	if err := h.deliver(); !errors.Is(err, ErrOutcomeAmbiguous) || errors.Is(err, ErrInvoiceRejected) {
		t.Fatalf("deliver = %v; want ErrOutcomeAmbiguous and no rejection", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "prepare", "book", "build", "commit", "submit", "record", "complete")
	if len(h.chain.recorded) != 1 || h.chain.recorded[0].State != OutcomeAmbiguous {
		t.Fatalf("recorded = %+v; want one ambiguous outcome and no rejection", h.chain.recorded)
	}
	if h.acks != 1 {
		t.Fatalf("acks = %d; want 1", h.acks)
	}

	h.calls = nil
	if err := h.deliver(); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "complete")
	if len(h.pipeline.submitted) != 1 {
		t.Fatalf("submitted %d times; want once", len(h.pipeline.submitted))
	}
}

func TestDuplicateIsAcknowledgedWithoutWork(t *testing.T) {
	h := newHarness(t)
	h.claims.decision = ClaimDuplicate
	if err := h.deliver(); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	expectSteps(t, h.calls, "claim")
	if h.acks != 1 {
		t.Fatalf("acks = %d; want 1", h.acks)
	}
}

func TestConflictIsAcknowledgedAndReported(t *testing.T) {
	h := newHarness(t)
	h.claims.decision = ClaimConflict
	if err := h.deliver(); !errors.Is(err, ErrFingerprintConflict) {
		t.Fatalf("deliver = %v; want ErrFingerprintConflict", err)
	}
	expectSteps(t, h.calls, "claim")
	if h.acks != 1 {
		t.Fatalf("acks = %d; want 1", h.acks)
	}
}

func TestInFlightDeliveryIsLeftForLater(t *testing.T) {
	h := newHarness(t)
	h.claims.decision = ClaimInFlight
	if err := h.deliver(); !errors.Is(err, ErrClaimInFlight) {
		t.Fatalf("deliver = %v; want ErrClaimInFlight", err)
	}
	expectSteps(t, h.calls, "claim")
	if h.acks != 0 {
		t.Fatalf("acks = %d; want 0", h.acks)
	}
}

// A stored invoice of another UUID was committed earlier, so the mismatch is a technical failure for an operator. It is not
// an input error, and nothing is submitted.
func TestStoredInvoiceOfAnotherUUIDIsATechnicalFailure(t *testing.T) {
	h := newHarness(t)
	h.chain.stored = &SignedInvoice{
		SourceID:  42,
		BranchID:  7,
		UUID:      "11111111-2222-4333-8444-555555555555",
		ICV:       1,
		PIH:       GenesisPIH,
		Hash:      "hash-of-bill-42",
		Kind:      InvoiceStandard,
		SignedXML: "<Invoice/>",
	}
	err := h.deliver()
	if err == nil || errors.Is(err, ErrRejectedInput) {
		t.Fatalf("deliver = %v; want a technical error, not a rejected input", err)
	}
	expectSteps(t, h.calls, "claim", "stored", "unclaim")
	if len(h.pipeline.submitted) != 0 || h.acks != 0 {
		t.Fatal("a mismatched invoice was submitted or acknowledged")
	}
}

func TestEnvelopeWithoutItsInvoiceUUIDIsRefusedBeforeAnyWork(t *testing.T) {
	h := newHarness(t)
	h.msg.InvoiceUUID = ""
	if err := h.deliver(); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("deliver = %v; want ErrInvalidEnvelope", err)
	}
	expectSteps(t, h.calls)
	if h.acks != 0 {
		t.Fatalf("acks = %d; want 0", h.acks)
	}
}
