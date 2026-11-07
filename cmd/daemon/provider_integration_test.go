package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/models"
	"github.com/zatca-go/zatca/processor"
	"github.com/zatca-go/zatca/provider"
	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/tenant"
	"github.com/zatca-go/zatca/zatca"
)

type daemonPipelineClaims struct {
	mu        sync.Mutex
	claims    int
	completed int
	released  int
}

func (c *daemonPipelineClaims) Claim(context.Context, queue.Message) (queue.ClaimDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claims++
	return queue.ClaimProcess, nil
}

func (c *daemonPipelineClaims) Complete(context.Context, queue.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completed++
	return nil
}

func (c *daemonPipelineClaims) Release(context.Context, queue.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.released++
	return nil
}

type daemonPipelineChain struct {
	mu        sync.Mutex
	invoice   queue.SignedInvoice
	storedFor []queue.Message
	outcomes  []queue.Outcome
}

func (c *daemonPipelineChain) Stored(_ context.Context, msg queue.Message) (*queue.SignedInvoice, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.storedFor = append(c.storedFor, msg)
	invoice := c.invoice
	return &invoice, nil
}

func (c *daemonPipelineChain) Book(context.Context, queue.Message) (queue.Booking, error) {
	return queue.Booking{}, errors.New("book is not expected for a committed invoice")
}

func (c *daemonPipelineChain) Release(context.Context, queue.Booking) error {
	return errors.New("booking release is not expected for a committed invoice")
}

func (c *daemonPipelineChain) Commit(context.Context, queue.Booking, queue.SignedInvoice) error {
	return errors.New("commit is not expected for a committed invoice")
}

func (c *daemonPipelineChain) Record(_ context.Context, _ queue.SignedInvoice, outcome queue.Outcome) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invoice.Outcome = outcome.State
	c.invoice.OutcomeEvidence = outcome.Evidence
	c.invoice.EvidenceID = outcome.EvidenceID
	c.invoice.ProviderCode = outcome.ProviderCode
	c.invoice.OutcomeMessage = outcome.Message
	c.outcomes = append(c.outcomes, outcome)
	return nil
}

func (c *daemonPipelineChain) outcome() queue.Outcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.outcomes) == 0 {
		return queue.Outcome{}
	}
	return c.outcomes[len(c.outcomes)-1]
}

func daemonProviderMessage() queue.Message {
	return queue.Message{
		DocType:            queue.DocBill,
		ID:                 42,
		BranchID:           7,
		DBName:             "tenant_acme",
		TenantKey:          "acme-0001",
		PosID:              "pos-1",
		BillRequestID:      "3f2b9c1e-7a4d-4e8b-9c21-5d0e6f7a8b9c",
		EventID:            "9b1d2c3e-4f5a-4b6c-8d7e-0f1a2b3c4d5e",
		EGSSequenceID:      "7",
		InvoiceUUID:        "0c4f7a1e-2b3d-4e5f-8a9b-1c2d3e4f5a6b",
		RequestFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func daemonProviderInvoice(msg queue.Message) queue.SignedInvoice {
	return queue.SignedInvoice{
		SourceID:  msg.ID,
		BranchID:  msg.BranchID,
		UUID:      msg.InvoiceUUID,
		ICV:       1,
		PIH:       queue.GenesisPIH,
		Hash:      "hash-of-bill-42",
		Kind:      queue.InvoiceStandard,
		SignedXML: "<Invoice/>",
		QR:        "qr",
	}
}

func newDaemonProviderPipeline(
	t *testing.T,
	scenario provider.Scenario,
	msg queue.Message,
	stored queue.SignedInvoice,
) (*queue.OrderedStream, *daemonPipelineClaims, *daemonPipelineChain, *provider.Local, *tenantCtx) {
	t.Helper()

	factory, err := newProviderFactory("local", string(scenario))
	if err != nil {
		t.Fatalf("newProviderFactory = %v", err)
	}
	adapter, err := factory(msg.BranchID, nil, nil)
	if err != nil {
		t.Fatalf("provider factory = %v", err)
	}
	local, ok := adapter.(*provider.Local)
	if !ok {
		t.Fatalf("provider = %T; want *provider.Local", adapter)
	}

	cfg := &config.Config{
		Env:              config.Sandbox,
		ProviderEndpoint: "https://provider.test",
	}
	svc := &zatca.Service{Config: cfg}
	claims := &daemonPipelineClaims{}
	chain := &daemonPipelineChain{invoice: stored}
	pipeline := &processor.BillPipeline{
		Service: func(int64) (*zatca.Service, *config.Config, error) {
			return svc, cfg, nil
		},
		ProviderFactory: factory,
		RequestBuilder: func(_ *zatca.Service, signedXML string) (models.InvoiceRequest, error) {
			return models.InvoiceRequest{
				UUID:        stored.UUID,
				InvoiceHash: stored.Hash,
				Invoice:     signedXML,
			}, nil
		},
	}
	tc := &tenantCtx{
		tenant: tenant.Tenant{
			TenantKey: msg.TenantKey,
			DBName:    msg.DBName,
		},
	}
	return queue.NewOrderedStream(claims, chain, pipeline), claims, chain, local, tc
}

func deliverDaemonPipeline(t *testing.T, stream *queue.OrderedStream, tc *tenantCtx, msg queue.Message) bool {
	t.Helper()
	nc := startNATS(t)
	subject := fmt.Sprintf("zatca.bill.%s.%d", msg.DBName, msg.BranchID)
	deliveries, err := nc.SubscribeSync(subject)
	if err != nil {
		t.Fatal(err)
	}
	reply := nats.NewInbox()
	acks, err := nc.SubscribeSync(reply)
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.PublishMsg(&nats.Msg{Subject: subject, Reply: reply, Data: mustJSON(t, msg)}); err != nil {
		t.Fatal(err)
	}
	raw, err := deliveries.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("delivery did not arrive: %v", err)
	}

	handleDelivery(stream, tc, msg.DBName, msg.BranchID, raw)
	ack, err := acks.NextMsg(250 * time.Millisecond)
	return err == nil && string(ack.Data) == "+ACK"
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDaemonProviderPipelineScenarios(t *testing.T) {
	tests := []struct {
		name        string
		scenario    provider.Scenario
		wantState   string
		wantCode    string
		wantAck     bool
		wantRelease int
	}{
		{name: "accepted", scenario: provider.ScenarioAccepted, wantState: queue.OutcomeAccepted, wantAck: true},
		{name: "warning", scenario: provider.ScenarioWarning, wantState: queue.OutcomeWarning, wantCode: "LOCAL-WARN", wantAck: true},
		{name: "rejected", scenario: provider.ScenarioRejected, wantState: queue.OutcomeRejected, wantCode: "LOCAL-REJECT", wantAck: true},
		{name: "timeout", scenario: provider.ScenarioTimeout, wantAck: false, wantRelease: 1},
		{name: "409", scenario: provider.ScenarioConflict, wantState: queue.OutcomeAmbiguous, wantCode: "409", wantAck: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := daemonProviderMessage()
			invoice := daemonProviderInvoice(msg)
			stream, claims, chain, local, tc := newDaemonProviderPipeline(t, tt.scenario, msg, invoice)

			if got := deliverDaemonPipeline(t, stream, tc, msg); got != tt.wantAck {
				t.Fatalf("acknowledged = %t; want %t", got, tt.wantAck)
			}
			outcome := chain.outcome()
			if outcome.State != tt.wantState || outcome.ProviderCode != tt.wantCode {
				t.Fatalf("outcome = %+v; want state=%q code=%q", outcome, tt.wantState, tt.wantCode)
			}
			if claims.released != tt.wantRelease {
				t.Fatalf("released = %d; want %d", claims.released, tt.wantRelease)
			}
			if tt.wantAck && claims.completed != 1 {
				t.Fatalf("completed = %d; want one completed claim", claims.completed)
			}
			if len(local.Submissions()) != 1 {
				t.Fatalf("submissions = %d; want one", len(local.Submissions()))
			}
		})
	}
}

func TestDaemonProviderPipelineReplaySubmitsCommittedIdentityAgain(t *testing.T) {
	msg := daemonProviderMessage()
	invoice := daemonProviderInvoice(msg)
	stream, claims, chain, local, tc := newDaemonProviderPipeline(t, provider.ScenarioCrashAfterAcceptance, msg, invoice)

	if deliverDaemonPipeline(t, stream, tc, msg) {
		t.Fatal("first replay delivery was acknowledged after a technical failure")
	}
	if got := chain.outcome(); got.State != "" {
		t.Fatalf("first outcome = %+v; want no recorded outcome", got)
	}
	if claims.released != 1 {
		t.Fatalf("released after first delivery = %d; want one", claims.released)
	}

	if !deliverDaemonPipeline(t, stream, tc, msg) {
		t.Fatal("second replay delivery was not acknowledged")
	}
	if got := chain.outcome(); got.State != queue.OutcomeAccepted {
		t.Fatalf("second outcome = %+v; want accepted", got)
	}
	if claims.completed != 1 {
		t.Fatalf("completed = %d; want one", claims.completed)
	}
	submissions := local.Submissions()
	if len(submissions) != 2 {
		t.Fatalf("submissions = %d; want two attempts", len(submissions))
	}
	if submissions[0].Request.UUID != invoice.UUID || submissions[1].Request.UUID != invoice.UUID {
		t.Fatalf("submission UUIDs = %q, %q; want %q", submissions[0].Request.UUID, submissions[1].Request.UUID, invoice.UUID)
	}
}

func TestDaemonProviderPipelinePreservesInvoiceIdentity(t *testing.T) {
	msg := daemonProviderMessage()
	invoice := daemonProviderInvoice(msg)
	stream, _, chain, local, tc := newDaemonProviderPipeline(t, provider.ScenarioAccepted, msg, invoice)

	if !deliverDaemonPipeline(t, stream, tc, msg) {
		t.Fatal("identity delivery was not acknowledged")
	}
	if got := chain.outcome(); got.State != queue.OutcomeAccepted {
		t.Fatalf("outcome = %+v; want accepted", got)
	}
	submissions := local.Submissions()
	if len(submissions) != 1 {
		t.Fatalf("submissions = %d; want one", len(submissions))
	}
	submission := submissions[0]
	if submission.Request.UUID != invoice.UUID {
		t.Fatalf("provider UUID = %q; want stored UUID %q", submission.Request.UUID, invoice.UUID)
	}
	if submission.Request.InvoiceHash != invoice.Hash {
		t.Fatalf("provider hash = %q; want stored hash %q", submission.Request.InvoiceHash, invoice.Hash)
	}
	if submission.Request.Invoice != invoice.SignedXML || submission.SignedXML != invoice.SignedXML {
		t.Fatalf("provider XML = request %q / signed %q; want %q", submission.Request.Invoice, submission.SignedXML, invoice.SignedXML)
	}
	if submission.Endpoint != "https://provider.test" || submission.Kind != provider.KindStandard {
		t.Fatalf("provider routing = endpoint %q kind %q; want endpoint %q kind %q",
			submission.Endpoint, submission.Kind, "https://provider.test", provider.KindStandard)
	}
}

func TestDaemonProviderPipelineRejectsStoredIdentityMismatch(t *testing.T) {
	msg := daemonProviderMessage()
	invoice := daemonProviderInvoice(msg)
	invoice.UUID = "11111111-2222-4333-8444-555555555555"
	stream, claims, chain, local, tc := newDaemonProviderPipeline(t, provider.ScenarioAccepted, msg, invoice)

	if deliverDaemonPipeline(t, stream, tc, msg) {
		t.Fatal("identity mismatch was acknowledged")
	}
	if len(local.Submissions()) != 0 {
		t.Fatalf("submissions = %d; want none", len(local.Submissions()))
	}
	if len(chain.outcomes) != 0 {
		t.Fatalf("outcomes = %+v; want none", chain.outcomes)
	}
	if claims.released != 1 {
		t.Fatalf("released = %d; want one", claims.released)
	}
}
