package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/queue"
)

func main() {
	natsURL := flag.String("nats-url", envOrDefault("NATS_URL", ""), "NATS URL")
	tenantKey := flag.String("tenant-key", "", "Stable tenant key")
	dbName := flag.String("db-name", "", "Tenant database name")
	branchID := flag.Int64("branch-id", 0, "Branch identifier")
	billID := flag.Int64("bill-id", 0, "Bill identifier")
	action := flag.String("action", queue.ReconcileActionStatus, "Reconciliation action: status or record")
	invoiceUUID := flag.String("invoice-uuid", "", "Stored invoice UUID, when known")
	invoiceHash := flag.String("invoice-hash", "", "Exact stored invoice hash (required for record)")
	state := flag.String("state", "", "External provider state for record")
	providerCode := flag.String("provider-code", "", "Provider code from external evidence")
	message := flag.String("message", "", "Provider message from external evidence")
	evidenceID := flag.String("evidence-id", "", "Operator evidence reference (required for record)")
	timeout := flag.Duration("timeout", 10*time.Second, "NATS request timeout")
	flag.Parse()

	if *natsURL == "" {
		log.Fatal("NATS URL is required: set --nats-url or NATS_URL")
	}
	req := queue.ReconcileRequest{
		TenantKey:    *tenantKey,
		DBName:       *dbName,
		BranchID:     *branchID,
		BillID:       *billID,
		Action:       *action,
		InvoiceUUID:  *invoiceUUID,
		InvoiceHash:  *invoiceHash,
		State:        *state,
		ProviderCode: *providerCode,
		Message:      *message,
		EvidenceID:   *evidenceID,
	}
	data, err := queue.EncodeReconcileRequest(req)
	if err != nil {
		log.Fatal(err)
	}
	nc, err := nats.Connect(*natsURL)
	if err != nil {
		log.Fatalf("connect to NATS: %v", err)
	}
	defer nc.Close()
	replyData, err := nc.Request(queue.ReconcileSubject(req.TenantKey), data, *timeout)
	if err != nil {
		log.Fatalf("reconciliation request failed: %v", err)
	}
	var reply queue.ReconcileReply
	if err := json.Unmarshal(replyData.Data, &reply); err != nil {
		log.Fatalf("decode reconciliation reply: %v", err)
	}
	encoded, err := json.MarshalIndent(reply, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
	if !reply.Accepted {
		os.Exit(1)
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
