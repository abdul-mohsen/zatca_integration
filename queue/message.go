package queue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// DocType identifies the fiscal document an envelope carries.
type DocType string

// DocBill is the only document the daemon accepts until credit and debit notes return (B6, Z9).
const DocBill DocType = "bill"

// StreamName is the JetStream stream that holds document envelopes.
const StreamName = "ZATCA"

// SubjectAll matches every document subject. Control requests use another prefix, so they never enter the stream.
const SubjectAll = "zatca.>"

// ErrInvalidEnvelope marks a message that can never be processed. Its delivery is terminated, not retried.
var ErrInvalidEnvelope = errors.New("invalid queue envelope")

// Message is the document envelope the backend publishes for one final sale. The backend sets every identity field, and the
// daemon reads them without creating any. The fiscal identity (seller VAT and EGS id) comes from the branch configuration.
type Message struct {
	DocType            DocType `json:"doc_type"`
	ID                 int64   `json:"id"`
	BranchID           int64   `json:"branch_id"`
	DBName             string  `json:"db_name"`
	TenantKey          string  `json:"tenant_key"`
	PosID              string  `json:"pos_id"`
	BillRequestID      string  `json:"bill_request_id"`
	EventID            string  `json:"event_id"`
	EGSSequenceID      string  `json:"egs_sequence_id"`
	InvoiceUUID        string  `json:"invoice_uuid"`
	RequestFingerprint string  `json:"request_fingerprint"`
}

// DecodeEnvelope decodes and validates one document envelope. Unknown fields are refused, so an envelope from another
// contract (one that still carries tenant_id, for example) fails as a whole instead of being read in part.
func DecodeEnvelope(data []byte) (Message, error) {
	var msg Message
	if err := strictDecode(data, &msg); err != nil {
		return Message{}, err
	}
	if err := msg.Validate(); err != nil {
		return Message{}, err
	}
	return msg, nil
}

// Validate checks the identity a document needs before it is claimed.
func (m Message) Validate() error {
	switch {
	case m.DocType != DocBill:
		return invalidf("unsupported doc_type %q", m.DocType)
	case m.ID <= 0:
		return invalidf("id is required")
	case m.BranchID <= 0:
		return invalidf("branch_id is required")
	case strings.TrimSpace(m.DBName) == "":
		return invalidf("db_name is required")
	case strings.TrimSpace(m.TenantKey) == "":
		return invalidf("tenant_key is required")
	case strings.TrimSpace(m.PosID) == "" || len(m.PosID) > 64:
		return invalidf("pos_id is required and must be at most 64 characters")
	case strings.TrimSpace(m.BillRequestID) == "":
		return invalidf("bill_request_id is required")
	case strings.TrimSpace(m.EventID) == "":
		return invalidf("event_id is required")
	case m.EGSSequenceID != strconv.FormatInt(m.BranchID, 10):
		return invalidf("egs_sequence_id must be the branch id")
	case !isUUID(m.InvoiceUUID):
		return invalidf("invoice_uuid must be a UUID")
	case strings.TrimSpace(m.RequestFingerprint) == "":
		return invalidf("request_fingerprint is required")
	}
	return nil
}

// RequestKey identifies one business request at its POS.
func (m Message) RequestKey() string {
	return m.PosID + "\x00" + m.BillRequestID
}

// PartitionKey is the ordered fiscal stream a document belongs to: its tenant and its EGS. Each branch is one EGS.
func (m Message) PartitionKey() string {
	return m.TenantKey + "\x00" + m.EGSSequenceID
}

// OrderedSubject matches every document of one branch, so one consumer receives the branch's documents in order.
func OrderedSubject(dbName string, branchID int64) string {
	return fmt.Sprintf("zatca.*.%s.%d", dbName, branchID)
}

// OrderedConsumerName is the durable consumer of one branch's documents.
func OrderedConsumerName(dbName string, branchID int64) string {
	return fmt.Sprintf("zatca_ordered_%s_%d", dbName, branchID)
}

func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEnvelope, err)
	}
	return nil
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidEnvelope, fmt.Sprintf(format, args...))
}

func isUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}
