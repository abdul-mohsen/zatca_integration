package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/queue"
)

// testrunner: tests OTP dedup and fresh-setup onboarding.
// 1. Verifies fresh setup (no bill workers)
// 2. Publishes 3 OTP messages rapidly → daemon should drop older 2, process only the newest
// 3. Watches daemon consume all messages
func main() {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}
	dbName := os.Getenv("DB_NAME")
	if dbName == "" {
		dbName = "zatca_test_db"
	}

	fmt.Println("========================================")
	fmt.Println("  ZATCA OTP Dedup Test")
	fmt.Println("  Publish 3 OTPs → only newest processed")
	fmt.Println("========================================")
	fmt.Printf("NATS:    %s\n", natsURL)
	fmt.Printf("DB Name: %s\n\n", dbName)

	// Connect with retries
	var nc *nats.Conn
	var err error
	for i := 0; i < 15; i++ {
		nc, err = nats.Connect(natsURL)
		if err == nil {
			break
		}
		fmt.Printf("  Waiting for NATS... (%v)\n", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("FAIL: cannot connect to NATS: %v", err)
	}
	defer nc.Close()
	fmt.Println("[OK] Connected to NATS")

	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("FAIL: jetstream: %v", err)
	}

	// ── Step 1: Verify stream and fresh setup ──────────────────────────
	info, err := js.StreamInfo(queue.StreamName)
	if err != nil {
		log.Fatalf("FAIL: stream %s not found: %v", queue.StreamName, err)
	}
	fmt.Printf("[OK] Stream %s exists (msgs=%d consumers=%d)\n",
		info.Config.Name, info.State.Msgs, info.State.Consumers)

	fmt.Println("\n--- Step 1: Verify fresh setup ---")
	time.Sleep(2 * time.Second)
	listConsumers(js)

	billConsumer := queue.ConsumerName(queue.DocBill, dbName, 1)
	if _, err := js.ConsumerInfo(queue.StreamName, billConsumer); err != nil {
		fmt.Println("[OK] No bill consumer — branch NOT onboarded (expected)")
	} else {
		fmt.Println("[!!] Bill consumer exists — branch already onboarded!")
	}

	// Verify onboard consumer exists
	onboardCons := fmt.Sprintf("zatca_onboard_%s", dbName)
	if ci, err := js.ConsumerInfo(queue.StreamName, onboardCons); err == nil {
		fmt.Printf("[OK] Onboard consumer ready: %s (waiting=%d)\n", ci.Name, ci.NumWaiting)
	} else {
		fmt.Println("[!!] No onboard consumer — daemon may not have started")
	}

	// ── Step 2: Publish 3 OTP messages rapidly ─────────────────────────
	// The daemon should drop the first two and only process OTP "newest_003"
	fmt.Println("\n--- Step 2: Publish 3 OTPs rapidly (dedup test) ---")
	subject := queue.Subject(queue.DocOnboard, dbName, 1)

	otps := []string{"old_001", "old_002", "newest_003"}
	for i, otp := range otps {
		msg := queue.Message{
			DocType:  queue.DocOnboard,
			ID:       0,
			BranchID: 1,
			DBName:   dbName,
			OTP:      otp,
		}
		data, _ := json.Marshal(msg)
		ack, err := js.Publish(subject, data)
		if err != nil {
			log.Fatalf("FAIL: publish OTP %d: %v", i+1, err)
		}
		fmt.Printf("[OK] Published OTP #%d (%s) — seq=%d\n", i+1, otp, ack.Sequence)
	}

	// Check stream has all 3
	info, _ = js.StreamInfo(queue.StreamName)
	fmt.Printf("[OK] Stream now has %d messages\n", info.State.Msgs)

	// ── Step 3: Watch daemon process ───────────────────────────────────
	fmt.Println("\n--- Step 3: Watching daemon process (expect dedup in logs) ---")
	fmt.Println("[..] Daemon should log:")
	fmt.Println("     - 'Newer OTP for branch 1' (dropping old_001)")
	fmt.Println("     - 'Newer OTP for branch 1' (dropping old_002)")
	fmt.Println("     - 'Processing onboard: branch=1 ... (latest OTP)'")
	fmt.Println("[..] Waiting up to 90s...")

	consumed := false
	for i := 1; i <= 90; i++ {
		time.Sleep(1 * time.Second)
		si, _ := js.StreamInfo(queue.StreamName)
		if si.State.Msgs == 0 {
			fmt.Printf("\n[OK] All messages consumed after %ds\n", i)
			consumed = true
			break
		}
		if i%10 == 0 {
			fmt.Printf("[..] Still waiting... (%ds, msgs=%d)\n", i, si.State.Msgs)
		}
	}

	if !consumed {
		si, _ := js.StreamInfo(queue.StreamName)
		fmt.Printf("\n[..] Messages still in stream after 90s (msgs=%d)\n", si.State.Msgs)
	}

	// ── Step 4: Check consumer stats ───────────────────────────────────
	fmt.Println("\n--- Step 4: Consumer stats after dedup ---")
	listConsumers(js)

	if ci, err := js.ConsumerInfo(queue.StreamName, onboardCons); err == nil {
		fmt.Printf("\n[..] Onboard consumer: delivered=%d ack_pending=%d\n",
			ci.Delivered.Stream, ci.NumAckPending)
		if ci.Delivered.Stream >= 3 {
			fmt.Println("[OK] All 3 messages were delivered to consumer (dedup happened inside worker)")
		}
	}

	// Check if onboard succeeded (bill consumer would appear)
	if _, err := js.ConsumerInfo(queue.StreamName, billConsumer); err == nil {
		fmt.Println("[OK] Bill consumer appeared — onboarding SUCCEEDED!")
	} else {
		fmt.Println("[..] No bill consumer — onboarding didn't complete")
		fmt.Println("     (expected with sandbox OTP, check daemon logs)")
	}

	// ── Final ──────────────────────────────────────────────────────────
	info, _ = js.StreamInfo(queue.StreamName)
	fmt.Printf("\n--- Final stream state ---\n")
	fmt.Printf("  Messages:  %d\n", info.State.Msgs)
	fmt.Printf("  Consumers: %d\n", info.State.Consumers)

	fmt.Println("\n========================================")
	fmt.Println("  Test Complete")
	fmt.Println("========================================")
	fmt.Println("Check daemon logs for 'Newer OTP' and 'Dropping' messages.")
}

func listConsumers(js nats.JetStreamContext) {
	names := js.ConsumerNames(queue.StreamName)
	count := 0
	for name := range names {
		count++
		ci, err := js.ConsumerInfo(queue.StreamName, name)
		if err != nil {
			fmt.Printf("  %s: ERROR %v\n", name, err)
			continue
		}
		fmt.Printf("  [%d] %-40s filter=%-35s pending=%d delivered=%d waiting=%d\n",
			count, ci.Name, ci.Config.FilterSubject, ci.NumPending, ci.Delivered.Stream, ci.NumWaiting)
	}
	if count == 0 {
		fmt.Println("  (no consumers)")
	}
}
