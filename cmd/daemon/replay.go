package main

import (
	"database/sql"
	"log"

	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/processor"
	"github.com/zatca-go/zatca/queue"
)

// replayPending publishes NATS messages for any documents still pending in the DB.
// This catches anything that was missed while the daemon was down or if the
// Gin server failed to publish.
func replayPending(js nats.JetStreamContext, db *sql.DB, tenantID uint64, dbName string, branches []int64) {
	total := 0
	for _, branchID := range branches {
		total += replayBranch(js, db, tenantID, dbName, branchID)
	}
	if total > 0 {
		log.Printf("  Replay: published %d pending messages for %s", total, dbName)
	}
}

func replayBranch(js nats.JetStreamContext, db *sql.DB, tenantID uint64, dbName string, branchID int64) int {
	messages, err := processor.QueryPendingOrderedMessages(db, tenantID, branchID)
	if err != nil {
		log.Printf("  Replay: cannot load ordered outbox for %s branch %d: %v", dbName, branchID, err)
		return 0
	}

	count := 0
	for _, msg := range messages {
		if publishReplay(js, msg) == nil {
			count++
		}
	}
	return count
}

func publishReplay(js nats.JetStreamContext, msg queue.Message) error {
	data, err := queue.Encode(msg)
	if err != nil {
		return err
	}
	// Use PublishMsgID for deduplication — if the message was already published by Gin,
	// JetStream will skip the duplicate.
	_, err = js.Publish(msg.Subject(), data, nats.MsgId(msg.EventID))
	return err
}
