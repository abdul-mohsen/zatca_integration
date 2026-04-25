// Package sample shows how to integrate the ZATCA daemon into your backend.
// Copy and adapt this into your own codebase.
package sample

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Message is the payload the ZATCA daemon expects.
type Message struct {
	DocType  string `json:"doc_type"`
	ID       int64  `json:"id"`
	BranchID int64  `json:"branch_id"`
	DBName   string `json:"db_name"`
	OTP      string `json:"otp,omitempty"`
}

// ZATCAPublisher publishes invoicing messages to the ZATCA daemon via NATS.
//
// It reads connection details from environment variables:
//
//	NATS_URL  — NATS server (default "nats://localhost:4222")
//	DBNAME    — tenant database name, used as db_name in messages
//
// The publisher automatically reconnects if the NATS server goes down and
// comes back up. Publishes made while disconnected return an error — the
// caller should retry or persist the request.
type ZATCAPublisher struct {
	js     nats.JetStreamContext
	nc     *nats.Conn
	dbName string
	once   sync.Once
}

// NewZATCAPublisher creates a publisher using environment variables for configuration.
// The connection will automatically reconnect if the NATS server restarts.
func NewZATCAPublisher() (*ZATCAPublisher, error) {
	natsURL := env("NATS_URL", "nats://localhost:4222")
	dbName := env("DBNAME", "")
	if dbName == "" {
		return nil, fmt.Errorf("DBNAME env var is required")
	}

	nc, err := nats.Connect(natsURL,
		// Keep retrying forever — the daemon may restart at any time.
		nats.MaxReconnects(-1),
		// Wait 2 seconds between reconnect attempts.
		nats.ReconnectWait(2*time.Second),
		// Buffer up to 8 MB of pending data during a reconnect window.
		nats.ReconnectBufSize(8*1024*1024),
		// Initial connect timeout.
		nats.Timeout(5*time.Second),
		// Log reconnection events so operators can see what's happening.
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Printf("[zatca-publisher] NATS disconnected: %v", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Printf("[zatca-publisher] NATS reconnected to %s", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			log.Printf("[zatca-publisher] NATS connection closed")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}

	return &ZATCAPublisher{js: js, nc: nc, dbName: dbName}, nil
}

// IsConnected returns true if the NATS connection is active.
func (p *ZATCAPublisher) IsConnected() bool {
	return p.nc.IsConnected()
}

// Close releases the NATS connection. Safe to call multiple times.
func (p *ZATCAPublisher) Close() {
	p.once.Do(func() { p.nc.Close() })
}

// SubmitBill queues a bill for ZATCA reporting/clearance.
// Returns an error if NATS is currently disconnected — caller should retry.
func (p *ZATCAPublisher) SubmitBill(billID, branchID int64) error {
	return p.publish(Message{DocType: "bill", ID: billID, BranchID: branchID, DBName: p.dbName})
}

// SubmitCredit queues a credit note for ZATCA reporting.
func (p *ZATCAPublisher) SubmitCredit(creditNoteID, branchID int64) error {
	return p.publish(Message{DocType: "credit", ID: creditNoteID, BranchID: branchID, DBName: p.dbName})
}

// SubmitDebit queues a debit note for ZATCA reporting.
func (p *ZATCAPublisher) SubmitDebit(debitNoteID, branchID int64) error {
	return p.publish(Message{DocType: "debit", ID: debitNoteID, BranchID: branchID, DBName: p.dbName})
}

// OnboardBranch registers a branch with ZATCA using the given OTP from the Fatoora portal.
func (p *ZATCAPublisher) OnboardBranch(branchID int64, otp string) error {
	return p.publish(Message{DocType: "onboard", ID: branchID, BranchID: branchID, DBName: p.dbName, OTP: otp})
}

func (p *ZATCAPublisher) publish(msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	subject := fmt.Sprintf("zatca.%s.%s.%d", msg.DocType, msg.DBName, msg.BranchID)
	_, err = p.js.Publish(subject, data)
	if err != nil {
		return fmt.Errorf("publish to %s: %w", subject, err)
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
