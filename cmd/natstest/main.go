package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/queue"
)

// Self-contained NATS pipeline test:
//  1. Start embedded NATS + JetStream
//  2. Create the ZATCA stream
//  3. Create a pull consumer like the daemon does
//  4. Publish an onboard message
//  5. Verify the consumer receives it
//
// No database needed.

func main() {
	// 1. Start embedded NATS
	tmpDir, _ := os.MkdirTemp("", "nats-test-*")
	defer os.RemoveAll(tmpDir)

	opts := &server.Options{
		Port:      -1, // random port
		JetStream: true,
		StoreDir:  tmpDir,
		NoLog:     true,
		NoSigs:    true,
	}
	ns, err := server.NewServer(opts)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		log.Fatal("server not ready")
	}
	defer ns.Shutdown()
	fmt.Printf("[OK] NATS server started on %s\n", ns.ClientURL())

	// 2. Connect
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	// 3. Create the ZATCA stream (same as daemon main.go)
	_, err = js.AddStream(&nats.StreamConfig{
		Name:      queue.StreamName,
		Subjects:  []string{queue.SubjectAll},
		Retention: nats.WorkQueuePolicy,
		Storage:   nats.FileStorage,
		MaxAge:    7 * 24 * time.Hour,
	})
	if err != nil {
		log.Fatalf("add stream: %v", err)
	}
	fmt.Println("[OK] Stream ZATCA created")

	// 4. Create a pull consumer (same as daemon onboard worker)
	dbName := "test_db"
	subject := fmt.Sprintf("zatca.onboard.%s.*", dbName)
	consName := fmt.Sprintf("zatca_onboard_%s", dbName)

	sub, err := js.PullSubscribe(subject, consName, nats.AckWait(5*time.Minute))
	if err != nil {
		log.Fatalf("subscribe: %v", err)
	}
	fmt.Printf("[OK] Consumer subscribed: subject=%s consumer=%s\n", subject, consName)

	// 5. Check stream info before publish
	info, _ := js.StreamInfo(queue.StreamName)
	fmt.Printf("[..] Stream msgs before publish: %d\n", info.State.Msgs)

	// 6. Publish an onboard message
	msg := queue.Message{
		DocType:  queue.DocOnboard,
		ID:       0,
		BranchID: 1,
		DBName:   dbName,
		OTP:      "123456",
	}
	data, _ := json.Marshal(msg)
	pubSubject := msg.Subject()
	fmt.Printf("[..] Publishing to: %s\n", pubSubject)
	fmt.Printf("[..] Payload: %s\n", string(data))

	ack, err := js.Publish(pubSubject, data)
	if err != nil {
		log.Fatalf("publish: %v", err)
	}
	fmt.Printf("[OK] Published — stream=%s seq=%d\n", ack.Stream, ack.Sequence)

	// 7. Check stream info after publish
	info, _ = js.StreamInfo(queue.StreamName)
	fmt.Printf("[..] Stream msgs after publish: %d\n", info.State.Msgs)

	// 8. Try to fetch the message (same as daemon pullAndProcess)
	fmt.Println("[..] Fetching message (5s timeout)...")
	msgs, err := sub.Fetch(1, nats.MaxWait(5*time.Second))
	if err != nil {
		log.Fatalf("[FAIL] Fetch returned error: %v", err)
	}

	if len(msgs) == 0 {
		log.Fatal("[FAIL] No messages received!")
	}

	// 9. Unmarshal and verify
	var received queue.Message
	if err := json.Unmarshal(msgs[0].Data, &received); err != nil {
		log.Fatalf("[FAIL] Unmarshal: %v", err)
	}
	msgs[0].Ack()

	fmt.Printf("[OK] Received: doc_type=%s branch_id=%d db_name=%s otp=%s\n",
		received.DocType, received.BranchID, received.DBName, received.OTP)

	// 10. Verify stream is empty after ack (WorkQueue policy)
	time.Sleep(500 * time.Millisecond)
	info, _ = js.StreamInfo(queue.StreamName)
	fmt.Printf("[..] Stream msgs after ack: %d\n", info.State.Msgs)

	// 11. Consumer info
	ci, _ := js.ConsumerInfo(queue.StreamName, consName)
	fmt.Printf("[..] Consumer delivered=%d ack_floor=%d pending=%d\n",
		ci.Delivered.Stream, ci.AckFloor.Stream, ci.NumPending)

	fmt.Println("\n=== ALL TESTS PASSED ===")
	fmt.Println("The NATS pipeline works correctly.")
	fmt.Println("If the daemon isn't logging, the issue is outside NATS (DB/config/container).")
}
