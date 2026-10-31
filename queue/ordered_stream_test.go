package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestOrderedStreamPreservesInterleavedDocumentTypeOrder(t *testing.T) {
	claims := NewMemoryClaimStore()
	chain := NewMemoryEGSSequenceStore()
	var (
		mu    sync.Mutex
		order []DocType
	)
	stream := NewOrderedStream(claims, chain, func(_ context.Context, msg Message, reservation EGSSequenceReservation) (EGSSequenceCommitRequest, error) {
		mu.Lock()
		order = append(order, msg.DocType)
		mu.Unlock()
		return testChainCommit(msg, reservation, fmt.Sprintf("hash-%d", reservation.ICV)), nil
	})

	messages := []Message{
		orderedTestMessage(DocBill, 7, "egs-a", "request-bill", "event-bill", "uuid-bill"),
		orderedTestMessage(DocCredit, 7, "egs-a", "request-credit", "event-credit", "uuid-credit"),
		orderedTestMessage(DocDebit, 7, "egs-a", "request-debit", "event-debit", "uuid-debit"),
	}
	for _, msg := range messages {
		if err := stream.HandleMessage(context.Background(), msg, func() error { return nil }); err != nil {
			t.Fatalf("handle %s: %v", msg.DocType, err)
		}
	}

	if got, want := fmt.Sprint(order), "[bill credit debit]"; got != want {
		t.Fatalf("document order = %s, want %s", got, want)
	}
	documents := chain.Documents(msgPartition(messages[0]))
	if got, want := len(documents), 3; got != want {
		t.Fatalf("committed documents = %d, want %d", got, want)
	}
	for i, document := range documents {
		if got, want := document.ICV, uint64(i+1); got != want {
			t.Fatalf("document %d ICV = %d, want %d", i, got, want)
		}
		if got, want := document.PIH, fmt.Sprintf("hash-%d", i); i == 0 {
			if got != nil {
				t.Fatalf("first document PIH = %q, want nil", *got)
			}
		} else if got == nil || *got != want {
			t.Fatalf("document %d PIH = %v, want %q", i, got, want)
		}
	}
}

func TestOrderedStreamPartitionsConcurrentEGSSequences(t *testing.T) {
	claims := NewMemoryClaimStore()
	chain := NewMemoryEGSSequenceStore()
	started := make(chan string, 2)
	release := make(chan struct{})
	stream := NewOrderedStream(claims, chain, func(_ context.Context, msg Message, reservation EGSSequenceReservation) (EGSSequenceCommitRequest, error) {
		started <- msg.EGSSequenceID
		<-release
		return testChainCommit(msg, reservation, "hash-"+msg.EGSSequenceID), nil
	})

	var group sync.WaitGroup
	errs := make(chan error, 2)
	for _, sequenceID := range []string{"egs-a", "egs-b"} {
		group.Add(1)
		go func(sequenceID string) {
			defer group.Done()
			msg := orderedTestMessage(DocBill, 7, sequenceID, "request-"+sequenceID, "event-"+sequenceID, "uuid-"+sequenceID)
			errs <- stream.HandleMessage(context.Background(), msg, func() error { return nil })
		}(sequenceID)
	}

	select {
	case first := <-started:
		select {
		case second := <-started:
			if first == second {
				t.Fatalf("same EGS sequence started twice: %s", first)
			}
		case <-time.After(time.Second):
			t.Fatal("second EGS sequence did not run concurrently")
		}
	case <-time.After(time.Second):
		t.Fatal("first EGS sequence did not start")
	}
	close(release)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, sequenceID := range []string{"egs-a", "egs-b"} {
		documents := chain.Documents(orderedTestMessage(DocBill, 7, sequenceID, "request-"+sequenceID, "event-"+sequenceID, "uuid-"+sequenceID).PartitionKey())
		if len(documents) != 1 || documents[0].ICV != 1 {
			t.Fatalf("%s documents = %#v, want one document with ICV 1", sequenceID, documents)
		}
	}
}

func TestOrderedStreamDuplicateDeliveryDoesNotAdvanceChain(t *testing.T) {
	claims := NewMemoryClaimStore()
	chain := NewMemoryEGSSequenceStore()
	var effects, acks int
	var mu sync.Mutex
	stream := NewOrderedStream(claims, chain, func(_ context.Context, msg Message, reservation EGSSequenceReservation) (EGSSequenceCommitRequest, error) {
		mu.Lock()
		effects++
		mu.Unlock()
		return testChainCommit(msg, reservation, "hash-duplicate"), nil
	})
	msg := orderedTestMessage(DocBill, 7, "egs-a", "request-1", "event-1", "uuid-1")

	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := stream.HandleMessage(context.Background(), msg, func() error {
				mu.Lock()
				acks++
				mu.Unlock()
				return nil
			}); err != nil {
				t.Errorf("duplicate delivery: %v", err)
			}
		}()
	}
	group.Wait()

	if effects != 1 {
		t.Fatalf("business effects = %d, want 1", effects)
	}
	if acks != 2 {
		t.Fatalf("acks = %d, want 2", acks)
	}
	if documents := chain.Documents(msg.PartitionKey()); len(documents) != 1 || documents[0].ICV != 1 {
		t.Fatalf("committed documents = %#v, want one document with ICV 1", documents)
	}
}

func TestOrderedStreamRetriesAfterCrashBeforeChainCommit(t *testing.T) {
	now := time.Unix(100, 0)
	claims := NewMemoryClaimStore()
	claims.LeaseDuration = time.Second
	claims.Now = func() time.Time { return now }
	chain := NewMemoryEGSSequenceStore()
	chain.LeaseDuration = time.Second
	chain.Now = func() time.Time { return now }
	var attempts int
	stream := NewOrderedStream(claims, chain, func(_ context.Context, msg Message, reservation EGSSequenceReservation) (EGSSequenceCommitRequest, error) {
		attempts++
		if attempts == 1 {
			return EGSSequenceCommitRequest{}, errors.New("crash before chain commit")
		}
		return testChainCommit(msg, reservation, "hash-retry"), nil
	})
	msg := orderedTestMessage(DocCredit, 7, "egs-a", "request-1", "event-1", "uuid-1")

	if err := stream.HandleMessage(context.Background(), msg, func() error { t.Fatal("crashed delivery was ACKed"); return nil }); err == nil {
		t.Fatal("first delivery succeeded despite crash")
	}
	now = now.Add(2 * time.Second)
	if err := stream.HandleMessage(context.Background(), msg, func() error { return nil }); err != nil {
		t.Fatalf("redelivery after crash: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("effect attempts = %d, want 2", attempts)
	}
	if documents := chain.Documents(msg.PartitionKey()); len(documents) != 1 || documents[0].ICV != 1 {
		t.Fatalf("committed documents = %#v, want one retried document with ICV 1", documents)
	}
}

func TestOrderedStreamCrashAfterChainCommitIsIdempotent(t *testing.T) {
	now := time.Unix(100, 0)
	claims := NewMemoryClaimStore()
	claims.LeaseDuration = time.Second
	claims.Now = func() time.Time { return now }
	chain := NewMemoryEGSSequenceStore()
	chain.Now = func() time.Time { return now }
	var attempts int
	stream := NewOrderedStream(claims, chain, func(_ context.Context, msg Message, reservation EGSSequenceReservation) (EGSSequenceCommitRequest, error) {
		attempts++
		return testChainCommit(msg, reservation, "hash-after-commit"), nil
	})
	msg := orderedTestMessage(DocDebit, 7, "egs-a", "request-1", "event-1", "uuid-1")
	ackCalls := 0
	ack := func() error {
		ackCalls++
		if ackCalls == 1 {
			return errors.New("crash after chain commit")
		}
		return nil
	}

	if err := stream.HandleMessage(context.Background(), msg, ack); err == nil {
		t.Fatal("first delivery succeeded despite ACK crash")
	}
	now = now.Add(2 * time.Second)
	if err := stream.HandleMessage(context.Background(), msg, ack); err != nil {
		t.Fatalf("redelivery after ACK crash: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("effect attempts = %d, want 1 after committed-chain recovery", attempts)
	}
	if documents := chain.Documents(msg.PartitionKey()); len(documents) != 1 || documents[0].ICV != 1 {
		t.Fatalf("committed documents = %#v, want one idempotent document", documents)
	}
}

func orderedTestMessage(docType DocType, tenantID uint64, sequenceID, requestID, eventID, documentUUID string) Message {
	return Message{
		DocType:       docType,
		ID:            42,
		BranchID:      7,
		DBName:        fmt.Sprintf("tenant_%d", tenantID),
		TenantID:      tenantID,
		PosID:         "pos-1",
		BillRequestID: requestID,
		EventID:       eventID,
		EGSSequenceID: sequenceID,
		InvoiceUUID:   documentUUID,
		EGSID:         "egs-1",
		VATNumber:     "310000000000003",
	}
}

func msgPartition(msg Message) string {
	return msg.PartitionKey()
}

func testChainCommit(msg Message, reservation EGSSequenceReservation, hash string) EGSSequenceCommitRequest {
	pih := ""
	if reservation.PIH != nil {
		pih = *reservation.PIH
	}
	return EGSSequenceCommitRequest{
		Reservation:  reservation,
		DocumentType: string(msg.DocType),
		SourceID:     uint64(msg.ID),
		State:        EGSDocumentQueued,
		Snapshot: []byte(fmt.Sprintf(
			`{"source_id":%d,"uuid":%q,"icv":%d,"pih":%q,"hash":%q}`,
			msg.ID,
			reservation.DocumentUUID,
			reservation.ICV,
			pih,
			hash,
		)),
		DocumentHash: hash,
	}
}
