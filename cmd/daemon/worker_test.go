package main

import (
	"testing"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/queue"
)

func TestConfigForMessageCopiesStableEnvelopeContext(t *testing.T) {
	base := &config.Config{
		TenantID:   "tenant-a",
		MerchantID: 7,
		BranchID:   2,
	}
	msg := queue.Message{
		DocType:           queue.DocBill,
		ID:                41,
		BranchID:          2,
		DBName:            "tenant-a",
		Contract:          queue.ContractName,
		Version:           queue.ContractVersion,
		SchemaVersion:     queue.ContractVersion,
		TenantID:          "tenant-a",
		DocumentType:      queue.DocBill,
		DocumentID:        41,
		SourceID:          41,
		StableDocumentKey: "tenant-a:2:bill:41",
		EventID:           "evt-41",
		OutboxID:          123,
		Attempt:           2,
		AttemptCount:      1,
		AttemptID:         "123:2",
	}

	got := configForMessage(base, msg)
	if got == base {
		t.Fatal("message context must not mutate the cached branch config")
	}
	if got.StableDocumentKey != msg.StableDocumentKey ||
		got.EventID != msg.EventID ||
		got.OutboxID != msg.OutboxID ||
		got.Attempt != msg.Attempt ||
		got.AttemptCount != msg.AttemptCount ||
		got.AttemptID != msg.AttemptID {
		t.Fatalf("copied message context = %#v", got)
	}
	if got.BranchID != msg.BranchID || got.MerchantID != base.MerchantID {
		t.Fatalf("copied tenant/branch context = %#v", got)
	}
	if base.StableDocumentKey != "" || base.OutboxID != 0 || base.Attempt != 0 {
		t.Fatalf("base config was mutated: %#v", base)
	}
}
