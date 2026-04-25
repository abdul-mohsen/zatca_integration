package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/processor"
	"github.com/zatca-go/zatca/queue"
)

// replayPending publishes NATS messages for any documents still pending in the DB.
// This catches anything that was missed while the daemon was down or if the
// Gin server failed to publish.
func replayPending(js nats.JetStreamContext, db *sql.DB, dbName string, branches []int64) {
	total := 0
	for _, branchID := range branches {
		total += replayBranch(js, db, dbName, branchID)
	}
	if total > 0 {
		log.Printf("  Replay: published %d pending messages for %s", total, dbName)
	}
}

func replayBranch(js nats.JetStreamContext, db *sql.DB, dbName string, branchID int64) int {
	count := 0

	// Bills with state < 3
	if ids, err := processor.QueryPendingBills(db, branchID); err == nil {
		for _, id := range ids {
			if publishReplay(js, queue.DocBill, dbName, branchID, id) == nil {
				count++
			}
		}
	}

	// Credit notes with state = 1
	if ids, err := processor.QueryPendingCreditNotes(db, branchID); err == nil {
		for _, id := range ids {
			if publishReplay(js, queue.DocCredit, dbName, branchID, id) == nil {
				count++
			}
		}
	}

	// Debit notes with state = 1
	if ids, err := processor.QueryPendingDebitNotes(db, branchID); err == nil {
		for _, id := range ids {
			if publishReplay(js, queue.DocDebit, dbName, branchID, id) == nil {
				count++
			}
		}
	}

	return count
}

func publishReplay(js nats.JetStreamContext, docType queue.DocType, dbName string, branchID int64, id int64) error {
	msg := queue.Message{
		DocType:  docType,
		ID:       id,
		BranchID: branchID,
		DBName:   dbName,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	// Use PublishMsgID for deduplication — if the message was already published by Gin,
	// JetStream will skip the duplicate.
	_, err = js.Publish(msg.Subject(), data, nats.MsgId(fmt.Sprintf("%s-%d", msg.Subject(), id)))
	return err
}
