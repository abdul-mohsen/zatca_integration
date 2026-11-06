package main

import (
	"context"
	"errors"
	"testing"

	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/tenant"
)

type reconcileChain struct {
	invoice  queue.SignedInvoice
	recorded []queue.Outcome
}

func (c *reconcileChain) Stored(context.Context, queue.Message) (*queue.SignedInvoice, error) {
	invoice := c.invoice
	return &invoice, nil
}

func (c *reconcileChain) Book(context.Context, queue.Message) (queue.Booking, error) {
	return queue.Booking{}, errors.New("not used")
}

func (c *reconcileChain) Release(context.Context, queue.Booking) error { return nil }
func (c *reconcileChain) Commit(context.Context, queue.Booking, queue.SignedInvoice) error {
	return errors.New("not used")
}
func (c *reconcileChain) Record(_ context.Context, _ queue.SignedInvoice, outcome queue.Outcome) error {
	c.recorded = append(c.recorded, outcome)
	return nil
}

func TestReconcileHandlerRequiresTheStoredHash(t *testing.T) {
	chain := &reconcileChain{invoice: queue.SignedInvoice{
		SourceID:  42,
		BranchID:  7,
		UUID:      "0c4f7a1e-2b3d-4e5f-8a9b-1c2d3e4f5a6b",
		Hash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ICV:       1,
		PIH:       queue.GenesisPIH,
		Kind:      queue.InvoiceStandard,
		SignedXML: "<Invoice/>",
	}}
	tc := &tenantCtx{
		tenant: tenant.Tenant{TenantKey: "acme-0001", DBName: "tenant_acme"},
		chain:  chain,
	}
	r := &daemonReconciler{}
	reply := r.handleReconcileRequest(context.Background(), tc, queue.ReconcileRequest{
		TenantKey:   "acme-0001",
		DBName:      "tenant_acme",
		BranchID:    7,
		BillID:      42,
		Action:      queue.ReconcileActionRecord,
		State:       queue.OutcomeAccepted,
		InvoiceHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		EvidenceID:  "case-42",
	})
	if reply.Accepted || reply.Error == "" {
		t.Fatalf("reply = %+v; want a hash mismatch refusal", reply)
	}
	if len(chain.recorded) != 0 {
		t.Fatalf("recorded = %+v; want no provider result without the stored hash", chain.recorded)
	}
}

func TestReconcileHandlerMarksSuppliedAnswerAsExternal(t *testing.T) {
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	chain := &reconcileChain{invoice: queue.SignedInvoice{
		SourceID:  42,
		BranchID:  7,
		UUID:      "0c4f7a1e-2b3d-4e5f-8a9b-1c2d3e4f5a6b",
		Hash:      hash,
		ICV:       1,
		PIH:       queue.GenesisPIH,
		Kind:      queue.InvoiceStandard,
		SignedXML: "<Invoice/>",
		Outcome:   queue.OutcomeAmbiguous,
	}}
	tc := &tenantCtx{
		tenant: tenant.Tenant{TenantKey: "acme-0001", DBName: "tenant_acme"},
		chain:  chain,
	}
	r := &daemonReconciler{}
	reply := r.handleReconcileRequest(context.Background(), tc, queue.ReconcileRequest{
		TenantKey:    "acme-0001",
		DBName:       "tenant_acme",
		BranchID:     7,
		BillID:       42,
		Action:       queue.ReconcileActionRecord,
		InvoiceHash:  hash,
		State:        queue.OutcomeAccepted,
		ProviderCode: "CLEARED",
		Message:      "verified externally",
		EvidenceID:   "case-42",
	})
	if !reply.Accepted || reply.Evidence != "external" || reply.EvidenceID != "case-42" {
		t.Fatalf("reply = %+v; want accepted external evidence", reply)
	}
	if len(chain.recorded) != 1 || chain.recorded[0].Evidence != queue.EvidenceExternal ||
		chain.recorded[0].EvidenceID != "case-42" {
		t.Fatalf("recorded = %+v; want external evidence id case-42", chain.recorded)
	}
}
