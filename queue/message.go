package queue

import "fmt"

// DocType identifies the type of document to process.
type DocType string

const (
	DocBill    DocType = "bill"
	DocCredit  DocType = "credit"
	DocDebit   DocType = "debit"
	DocOnboard DocType = "onboard"
)

// Message is the payload published to NATS when a new document needs processing.
type Message struct {
	DocType  DocType `json:"doc_type"`      // "bill", "credit", "debit", or "onboard"
	ID       int64   `json:"id"`            // primary key in the relevant table (bill_id for docs, branch_id for onboard)
	BranchID int64   `json:"branch_id"`     // branch for ordering
	DBName   string  `json:"db_name"`       // tenant database name
	OTP      string  `json:"otp,omitempty"` // OTP from Fatoora portal (onboard only)
}

// Subject returns the NATS subject for this message.
// Format: zatca.{doc_type}.{db_name}.{branch_id}
func (m Message) Subject() string {
	return Subject(m.DocType, m.DBName, m.BranchID)
}

// Subject builds a NATS subject from parts.
func Subject(docType DocType, dbName string, branchID int64) string {
	return fmt.Sprintf("zatca.%s.%s.%d", docType, dbName, branchID)
}

// StreamName is the JetStream stream that captures all ZATCA subjects.
const StreamName = "ZATCA"

// SubjectAll is the wildcard that matches all ZATCA subjects.
const SubjectAll = "zatca.>"

// ConsumerName builds a durable consumer name for a specific (tenant, branch, doc) combination.
func ConsumerName(docType DocType, dbName string, branchID int64) string {
	return fmt.Sprintf("zatca_%s_%s_%d", docType, dbName, branchID)
}
