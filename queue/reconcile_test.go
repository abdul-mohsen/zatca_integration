package queue

import (
	"errors"
	"strings"
	"testing"
)

func TestReconcileRequestRequiresExternalEvidenceForRecord(t *testing.T) {
	req := ReconcileRequest{
		TenantKey: "acme-0001",
		DBName:    "tenant_acme",
		BranchID:  7,
		BillID:    42,
		Action:    ReconcileActionRecord,
		State:     OutcomeAccepted,
	}
	if err := req.Validate(); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("Validate = %v; want ErrInvalidEnvelope", err)
	}
}

func TestReconcileRequestAcceptsAHashBoundExternalAnswer(t *testing.T) {
	req := ReconcileRequest{
		TenantKey:    "acme-0001",
		DBName:       "tenant_acme",
		BranchID:     7,
		BillID:       42,
		Action:       ReconcileActionRecord,
		InvoiceUUID:  "0c4f7a1e-2b3d-4e5f-8a9b-1c2d3e4f5a6b",
		InvoiceHash:  strings.Repeat("a", 64),
		State:        OutcomeWarning,
		ProviderCode: "W-1",
		Message:      "accepted with warning",
		EvidenceID:   "zatca-case-2026-11-06-42",
	}
	data, err := EncodeReconcileRequest(req)
	if err != nil {
		t.Fatalf("EncodeReconcileRequest: %v", err)
	}
	got, err := DecodeReconcileRequest(data)
	if err != nil {
		t.Fatalf("DecodeReconcileRequest: %v", err)
	}
	if got != req {
		t.Fatalf("decoded = %+v; want %+v", got, req)
	}
}

func TestReconcileRequestRejectsUnknownStateAndUnsafeEvidence(t *testing.T) {
	base := ReconcileRequest{
		TenantKey:   "acme-0001",
		DBName:      "tenant_acme",
		BranchID:    7,
		BillID:      42,
		Action:      ReconcileActionRecord,
		InvoiceHash: strings.Repeat("a", 64),
		EvidenceID:  "case-1",
	}
	for name, edit := range map[string]func(*ReconcileRequest){
		"unknown state": func(r *ReconcileRequest) { r.State = "accepted_without_evidence" },
		"control evidence": func(r *ReconcileRequest) {
			r.State = OutcomeAccepted
			r.EvidenceID = "case\n1"
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := base
			edit(&req)
			if err := req.Validate(); !errors.Is(err, ErrInvalidEnvelope) {
				t.Fatalf("Validate = %v; want ErrInvalidEnvelope", err)
			}
		})
	}
}
