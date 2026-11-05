package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/tenant"
)

// claimsOK claims every delivery and completes it.
type claimsOK struct{}

func (claimsOK) Claim(context.Context, queue.Message) (queue.ClaimDecision, error) {
	return queue.ClaimProcess, nil
}

func (claimsOK) Complete(context.Context, queue.Message) error { return nil }

func (claimsOK) Release(context.Context, queue.Message) error { return nil }

// committedChain holds the bill's invoice already committed, so the delivery goes straight to submission.
type committedChain struct {
	invoice queue.SignedInvoice
}

func (c committedChain) Stored(context.Context, queue.Message) (*queue.SignedInvoice, error) {
	return &c.invoice, nil
}

func (committedChain) Book(context.Context, queue.Message) (queue.Booking, error) {
	return queue.Booking{}, errors.New("book is not expected: the invoice is committed")
}

func (committedChain) Release(context.Context, queue.Booking) error { return nil }

func (committedChain) Commit(context.Context, queue.Booking, queue.SignedInvoice) error {
	return errors.New("commit is not expected: the invoice is committed")
}

func (committedChain) Record(context.Context, queue.SignedInvoice, queue.Outcome) error { return nil }

// ambiguousPipeline answers a submission the way ZATCA answers an invoice it already holds: a 409 that does not say whether
// the invoice is accepted.
type ambiguousPipeline struct{}

func (ambiguousPipeline) Prepare(context.Context, queue.Message) (queue.Prepared, error) {
	return nil, errors.New("prepare is not expected: the invoice is committed")
}

func (ambiguousPipeline) Submit(_ context.Context, invoice queue.SignedInvoice) (queue.Outcome, error) {
	outcome := queue.Outcome{State: queue.OutcomeAmbiguous, ProviderCode: "409", Message: "already reported"}
	return outcome, fmt.Errorf("%w: bill %d (invoice %s): ZATCA already holds this invoice",
		queue.ErrOutcomeAmbiguous, invoice.SourceID, invoice.UUID)
}

// startNATS runs an embedded NATS server on a free port and returns a client connected to it.
func startNATS(t *testing.T) *nats.Conn {
	t.Helper()
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	t.Cleanup(ns.Shutdown)
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded NATS server is not ready")
	}
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestAmbiguousAnswerAlertsOnceNamingTheBillAndIsAcknowledged(t *testing.T) {
	msg := queue.Message{
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
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

	var alerts []string
	original := sendAlertEmail
	sendAlertEmail = func(_, what, _ string, _ error) { alerts = append(alerts, what) }
	t.Cleanup(func() { sendAlertEmail = original })

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
	if err := nc.PublishMsg(&nats.Msg{Subject: subject, Reply: reply, Data: data}); err != nil {
		t.Fatal(err)
	}
	raw, err := deliveries.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("delivery did not arrive: %v", err)
	}

	stream := queue.NewOrderedStream(claimsOK{}, committedChain{invoice: queue.SignedInvoice{
		SourceID:  msg.ID,
		BranchID:  msg.BranchID,
		UUID:      msg.InvoiceUUID,
		ICV:       1,
		PIH:       queue.GenesisPIH,
		Hash:      "hash-of-bill-42",
		Kind:      queue.InvoiceStandard,
		SignedXML: "<Invoice/>",
		QR:        "qr",
	}}, ambiguousPipeline{})
	tc := &tenantCtx{tenant: tenant.Tenant{TenantKey: msg.TenantKey, DBName: msg.DBName, AlertEmail: "ops@example.com"}}

	handleDelivery(stream, tc, msg.DBName, msg.BranchID, raw)

	if len(alerts) != 1 {
		t.Fatalf("alerts = %q; want exactly one", alerts)
	}
	if !strings.Contains(alerts[0], "bill 42 of branch 7") || !strings.Contains(alerts[0], "needs reconciliation") {
		t.Fatalf("alert = %q; want it to name bill 42 and branch 7 and say the outcome needs reconciliation", alerts[0])
	}
	ack, err := acks.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("delivery was not acknowledged: %v", err)
	}
	if string(ack.Data) != "+ACK" {
		t.Fatalf("reply = %q; want +ACK, so the delivery is not retried", ack.Data)
	}
}
