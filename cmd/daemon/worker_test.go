package main

import (
	"testing"
	"time"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/processor"
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

func TestProcessWithRetryFuncStopsOnPermanentFailure(t *testing.T) {
	msg := queue.Message{
		DocType:           queue.DocBill,
		ID:                41,
		BranchID:          2,
		DBName:            "tenant-a",
		StableDocumentKey: "tenant-a:2:bill:41",
	}
	attempts := 0
	sleeps := 0
	err := processWithRetryFunc(
		msg,
		3,
		func(time.Duration) { sleeps++ },
		func(queue.Message) error {
			attempts++
			return &processor.ProviderError{
				State:   processor.OutcomeRejected,
				Code:    "HTTP_400",
				Message: "invalid invoice",
			}
		},
	)
	if err == nil {
		t.Fatal("expected permanent failure")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if sleeps != 0 {
		t.Fatalf("sleeps = %d, want 0", sleeps)
	}
}

func TestProcessWithRetryFuncMarksFinalTransientAttemptDeadLetter(t *testing.T) {
	msg := queue.Message{
		DocType:           queue.DocBill,
		ID:                41,
		BranchID:          2,
		DBName:            "tenant-a",
		StableDocumentKey: "tenant-a:2:bill:41",
	}
	var seen []queue.Message
	sleeps := 0
	err := processWithRetryFunc(
		msg,
		3,
		func(time.Duration) { sleeps++ },
		func(attempt queue.Message) error {
			seen = append(seen, attempt)
			return &processor.ProviderError{
				State:   processor.OutcomeAmbiguous,
				Code:    "TIMEOUT",
				Message: "response lost",
			}
		},
	)
	if err == nil {
		t.Fatal("expected exhausted transient failure")
	}
	if len(seen) != 3 {
		t.Fatalf("attempts = %d, want 3", len(seen))
	}
	if sleeps != 2 {
		t.Fatalf("sleeps = %d, want 2", sleeps)
	}
	if seen[0].RetryAttempt != 1 || seen[1].RetryAttempt != 2 || seen[2].RetryAttempt != 3 {
		t.Fatalf("attempt metadata = %#v", seen)
	}
	if !seen[2].DeadLetter {
		t.Fatal("final attempt should carry dead-letter context")
	}
}
