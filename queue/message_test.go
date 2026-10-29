package queue

import "testing"

func TestMessageNormalizesT014EnvelopeAndLegacyAlias(t *testing.T) {
	envelope := Message{
		DocType:           DocBill,
		ID:                42,
		BranchID:          7,
		DBName:            "tenant-a",
		Contract:          ContractName,
		Version:           ContractVersion,
		SchemaVersion:     ContractVersion,
		EventID:           "evt-1",
		TenantID:          "tenant-a",
		EGSID:             "tenant-a:branch:7",
		DocumentType:      DocBill,
		DocumentID:        42,
		SourceID:          42,
		OutboxID:          123,
		StableDocumentKey: "tenant-a:7:bill:42",
		Attempt:           2,
		AttemptCount:      1,
		AttemptID:         "123:2",
	}

	normalized, err := envelope.Normalize()
	if err != nil {
		t.Fatalf("normalize envelope: %v", err)
	}
	if err := normalized.Validate(); err != nil {
		t.Fatalf("validate envelope: %v", err)
	}
	if got := normalized.StableKey(); got != envelope.StableDocumentKey {
		t.Fatalf("stable key = %q, want %q", got, envelope.StableDocumentKey)
	}

	legacy, err := (Message{
		DocType:  DocBill,
		ID:       42,
		BranchID: 7,
		DBName:   "tenant-a",
	}).Normalize()
	if err != nil {
		t.Fatalf("normalize legacy message: %v", err)
	}
	if got := legacy.StableKey(); got != envelope.StableDocumentKey {
		t.Fatalf("legacy stable key = %q, want %q", got, envelope.StableDocumentKey)
	}
	if legacy.DocumentType != DocBill || legacy.SourceID != 42 || legacy.TenantID != "tenant-a" {
		t.Fatalf("legacy canonical fields = %#v", legacy)
	}
}

func TestMessageStableNATSIdentityIgnoresAttemptMetadata(t *testing.T) {
	first := Message{
		DocType:           DocBill,
		ID:                42,
		BranchID:          7,
		DBName:            "tenant-a",
		StableDocumentKey: "tenant-a:7:bill:42",
		Attempt:           1,
		AttemptID:         "123:1",
	}
	second := first
	second.Attempt = 3
	second.AttemptCount = 2
	second.AttemptID = "123:3"

	if first.NATSMessageID() != second.NATSMessageID() {
		t.Fatalf("message IDs differ across attempts: %q != %q", first.NATSMessageID(), second.NATSMessageID())
	}
	if first.NATSMessageID() != "tenant-a:7:bill:42" {
		t.Fatalf("message ID = %q, want stable document key", first.NATSMessageID())
	}
}
