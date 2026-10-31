package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

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

	// Canonical queue identity. These fields are required for document
	// messages and are the only fields used to decide whether a business
	// effect has already been claimed.
	TenantID           uint64 `json:"tenant_id,omitempty"`
	PosID              string `json:"pos_id,omitempty"`
	BillRequestID      string `json:"bill_request_id,omitempty"`
	EventID            string `json:"event_id,omitempty"`
	EGSSequenceID      string `json:"egs_sequence_id,omitempty"`
	RequestFingerprint string `json:"request_fingerprint,omitempty"`
	Fingerprint        string `json:"fingerprint,omitempty"`
	PayloadFingerprint string `json:"payload_fingerprint,omitempty"`

	rawFingerprint string
}

// Envelope is the canonical queue message shape. Message remains the
// concrete type for compatibility with the daemon's document processors.
type Envelope = Message

var (
	// ErrInvalidEnvelope indicates that a document delivery is missing a
	// canonical identity field or has an unsupported document type.
	ErrInvalidEnvelope = errors.New("invalid queue envelope")
)

// Encode serializes a queue message for transport.
func Encode(msg Message) ([]byte, error) {
	return json.Marshal(msg)
}

// Decode is a short alias for DecodeEnvelope.
func Decode(data []byte) (Message, error) {
	return DecodeEnvelope(data)
}

// DecodeCanonicalEnvelope decodes and validates a canonical document
// envelope. Onboard messages intentionally remain outside the document
// identity contract because they use a branch-scoped OTP instead.
func DecodeCanonicalEnvelope(data []byte) (Envelope, error) {
	return DecodeEnvelope(data)
}

// DecodeEnvelope decodes a message and validates its canonical identity.
func DecodeEnvelope(data []byte) (Message, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrInvalidEnvelope, err)
	}
	if raw == nil {
		return Message{}, fmt.Errorf("%w: expected JSON object", ErrInvalidEnvelope)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrInvalidEnvelope, err)
	}
	msg.rawFingerprint = fingerprintRawEnvelope(raw)
	if err := msg.Validate(); err != nil {
		return Message{}, err
	}
	return msg, nil
}

// Validate checks the canonical identity required for document consumption.
func (m Message) Validate() error {
	switch m.DocType {
	case DocOnboard:
		return nil
	case DocBill, DocCredit, DocDebit:
	default:
		return fmt.Errorf("%w: unsupported doc_type %q", ErrInvalidEnvelope, m.DocType)
	}

	switch {
	case m.TenantID == 0:
		return fmt.Errorf("%w: tenant_id is required", ErrInvalidEnvelope)
	case strings.TrimSpace(m.PosID) == "":
		return fmt.Errorf("%w: pos_id is required", ErrInvalidEnvelope)
	case len(m.PosID) > 64:
		return fmt.Errorf("%w: pos_id must be at most 64 characters", ErrInvalidEnvelope)
	case strings.TrimSpace(m.BillRequestID) == "":
		return fmt.Errorf("%w: bill_request_id is required", ErrInvalidEnvelope)
	case strings.TrimSpace(m.EventID) == "":
		return fmt.Errorf("%w: event_id is required", ErrInvalidEnvelope)
	case strings.TrimSpace(m.EGSSequenceID) == "":
		return fmt.Errorf("%w: egs_sequence_id is required", ErrInvalidEnvelope)
	}
	return nil
}

// RequestKey scopes a business request to the owning tenant and POS.
func (m Message) RequestKey() string {
	return fmt.Sprintf("%d\x00%s\x00%s", m.TenantID, m.PosID, m.BillRequestID)
}

// EventKey scopes a delivery event to the owning tenant.
func (m Message) EventKey() string {
	return fmt.Sprintf("%d\x00%s", m.TenantID, m.EventID)
}

// BusinessFingerprint returns the stable content identity used to compare
// replays. Explicit request fingerprints take precedence; when the producer
// has not supplied one, the decoded envelope is hashed without event_id so a
// redelivery with a new delivery ID remains a replay of the same request.
func (m Message) BusinessFingerprint() string {
	for _, value := range []string{
		m.RequestFingerprint,
		m.Fingerprint,
		m.PayloadFingerprint,
		m.rawFingerprint,
	} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}

	type canonicalMessage struct {
		DocType       DocType `json:"doc_type"`
		ID            int64   `json:"id"`
		BranchID      int64   `json:"branch_id"`
		DBName        string  `json:"db_name"`
		TenantID      uint64  `json:"tenant_id"`
		PosID         string  `json:"pos_id"`
		BillRequestID string  `json:"bill_request_id"`
		EGSSequenceID string  `json:"egs_sequence_id"`
	}
	data, _ := json.Marshal(canonicalMessage{
		DocType:       m.DocType,
		ID:            m.ID,
		BranchID:      m.BranchID,
		DBName:        m.DBName,
		TenantID:      m.TenantID,
		PosID:         m.PosID,
		BillRequestID: m.BillRequestID,
		EGSSequenceID: m.EGSSequenceID,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fingerprintRawEnvelope(raw map[string]json.RawMessage) string {
	normalized := make(map[string]json.RawMessage, len(raw))
	for key, value := range raw {
		switch key {
		case "event_id", "request_fingerprint", "fingerprint", "payload_fingerprint":
			continue
		default:
			normalized[key] = value
		}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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
