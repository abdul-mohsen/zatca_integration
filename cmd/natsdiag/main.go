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

func main() {
	natsURL := "nats://localhost:4222"
	if u := os.Getenv("NATS_URL"); u != "" {
		natsURL = u
	}

	if len(os.Args) < 2 {
		fmt.Println("Usage: natsdiag <command>")
		fmt.Println("  info       - show stream & consumer info")
		fmt.Println("  pub        - publish a test onboard message")
		fmt.Println("              args: <db_name> <branch_id> <otp>")
		fmt.Println("  listen     - subscribe to zatca.> and print messages for 60s")
		fmt.Println("  purge      - purge all messages from the ZATCA stream")
		fmt.Println("  consumers  - list all consumers and their state")
		os.Exit(1)
	}

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("connect %s: %v", natsURL, err)
	}
	defer nc.Close()
	fmt.Printf("Connected to %s\n\n", natsURL)

	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	switch os.Args[1] {
	case "info":
		cmdInfo(js)
	case "pub":
		if len(os.Args) < 5 {
			log.Fatal("usage: natsdiag pub <db_name> <branch_id> <otp>")
		}
		cmdPub(js, os.Args[2], os.Args[3], os.Args[4])
	case "listen":
		cmdListen(nc)
	case "purge":
		cmdPurge(js)
	case "consumers":
		cmdConsumers(js)
	default:
		log.Fatalf("unknown command: %s", os.Args[1])
	}
}

func cmdInfo(js nats.JetStreamContext) {
	info, err := js.StreamInfo(queue.StreamName)
	if err != nil {
		log.Fatalf("stream info: %v", err)
	}
	fmt.Printf("Stream: %s\n", info.Config.Name)
	fmt.Printf("  Subjects:    %v\n", info.Config.Subjects)
	fmt.Printf("  Retention:   %s\n", info.Config.Retention)
	fmt.Printf("  Messages:    %d\n", info.State.Msgs)
	fmt.Printf("  Bytes:       %d\n", info.State.Bytes)
	fmt.Printf("  FirstSeq:    %d\n", info.State.FirstSeq)
	fmt.Printf("  LastSeq:     %d\n", info.State.LastSeq)
	fmt.Printf("  Consumers:   %d\n", info.State.Consumers)
	fmt.Println()

	cmdConsumers(js)
}

func cmdConsumers(js nats.JetStreamContext) {
	fmt.Println("Consumers:")
	consNames := js.ConsumerNames(queue.StreamName)
	count := 0
	for name := range consNames {
		count++
		ci, err := js.ConsumerInfo(queue.StreamName, name)
		if err != nil {
			fmt.Printf("  %s: ERROR %v\n", name, err)
			continue
		}
		fmt.Printf("  [%d] %s\n", count, ci.Name)
		fmt.Printf("      Filter:        %s\n", ci.Config.FilterSubject)
		fmt.Printf("      AckPending:    %d\n", ci.NumAckPending)
		fmt.Printf("      NumPending:    %d\n", ci.NumPending)
		fmt.Printf("      Delivered:     seq=%d\n", ci.Delivered.Stream)
		fmt.Printf("      AckFloor:      seq=%d\n", ci.AckFloor.Stream)
		fmt.Printf("      NumWaiting:    %d (pull requests waiting)\n", ci.NumWaiting)
		fmt.Println()
	}
	if count == 0 {
		fmt.Println("  (none)")
	}
}

func cmdPub(js nats.JetStreamContext, dbName, branchIDStr, otp string) {
	var branchID int64
	fmt.Sscanf(branchIDStr, "%d", &branchID)

	msg := queue.Message{
		DocType:  queue.DocOnboard,
		ID:       0,
		BranchID: branchID,
		DBName:   dbName,
		OTP:      otp,
	}

	data, _ := json.Marshal(msg)
	subject := msg.Subject()

	fmt.Printf("Publishing to: %s\n", subject)
	fmt.Printf("Payload: %s\n", string(data))

	ack, err := js.Publish(subject, data)
	if err != nil {
		log.Fatalf("publish: %v", err)
	}
	fmt.Printf("Published OK — stream=%s seq=%d\n", ack.Stream, ack.Sequence)
}

func cmdListen(nc *nats.Conn) {
	fmt.Println("Listening on zatca.> for 60 seconds...")
	sub, err := nc.Subscribe("zatca.>", func(msg *nats.Msg) {
		fmt.Printf("[%s] %s\n", msg.Subject, string(msg.Data))
	})
	if err != nil {
		log.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	time.Sleep(60 * time.Second)
}

func cmdPurge(js nats.JetStreamContext) {
	err := js.PurgeStream(queue.StreamName)
	if err != nil {
		log.Fatalf("purge: %v", err)
	}
	fmt.Println("Stream purged")
}
