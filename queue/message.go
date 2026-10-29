package queue

import (
	"crypto/sha256"
	"encoding/hex"
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

const (
	ContractName    = "afrita.zatca.document-event"
	ContractVersion = "1"
)

// Message is the payload published to NATS when a new document needs processing.
//
// The first five fields are the legacy top-level aliases. The remaining fields
// are the additive T014 envelope and are intentionally kept top-level so an
// older daemon can continue decoding the message.
type Message struct {
	DocType  DocType `json:"doc_type"`      // "bill", "credit", "debit", or "onboard"
	ID       int64   `json:"id"`            // primary key in the relevant table (bill_id for docs, branch_id for onboard)
	BranchID int64   `json:"branch_id"`     // branch for ordering
	DBName   string  `json:"db_name"`       // tenant database name
	OTP      string  `json:"otp,omitempty"` // OTP from Fatoora portal (onboard only)

	Contract       string `json:"contract,omitempty"`
	Version        string `json:"version,omitempty"`
	SchemaVersion  string `json:"schema_version,omitempty"`
	EventID        string `json:"event_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`

	TenantID          string  `json:"tenant_id,omitempty"`
	EGSID             string  `json:"egs_id,omitempty"`
	DocumentType      DocType `json:"document_type,omitempty"`
	DocumentID        int64   `json:"document_id,omitempty"`
	SourceID          int64   `json:"source_id,omitempty"`
	OutboxID          uint64  `json:"outbox_id,omitempty"`
	StableDocumentKey string  `json:"stable_document_key,omitempty"`
	Attempt           uint32  `json:"attempt,omitempty"`
	AttemptCount      uint32  `json:"attempt_count,omitempty"`
	AttemptID         string  `json:"attempt_id,omitempty"`

	// RetryAttempt and DeadLetter are local worker context. They are not part
	// of the published T014 envelope and do not change stable identity.
	RetryAttempt uint32 `json:"-"`
	DeadLetter   bool   `json:"-"`
}

// Subject returns the NATS subject for this message.
// Format: zatca.{doc_type}.{db_name}.{branch_id}
func (m Message) Subject() string {
	docType := m.DocType
	if docType == "" {
		docType = m.DocumentType
	}
	dbName := strings.TrimSpace(m.DBName)
	if dbName == "" {
		dbName = strings.TrimSpace(m.TenantID)
	}
	return Subject(docType, dbName, m.BranchID)
}

// Normalize fills the canonical T014 fields from legacy aliases and derives
// deterministic identity/attempt fields. It does not reject unsupported
// document types; callers that need that guarantee should call Validate.
func (m Message) Normalize() (Message, error) {
	m.DocType = DocType(strings.TrimSpace(string(m.DocType)))
	m.DocumentType = DocType(strings.TrimSpace(string(m.DocumentType)))
	m.DBName = strings.TrimSpace(m.DBName)
	m.TenantID = strings.TrimSpace(m.TenantID)
	m.Contract = strings.TrimSpace(m.Contract)
	m.Version = strings.TrimSpace(m.Version)
	m.SchemaVersion = strings.TrimSpace(m.SchemaVersion)
	m.EventID = strings.TrimSpace(m.EventID)
	m.IdempotencyKey = strings.TrimSpace(m.IdempotencyKey)
	m.EGSID = strings.TrimSpace(m.EGSID)
	m.StableDocumentKey = strings.TrimSpace(m.StableDocumentKey)
	m.AttemptID = strings.TrimSpace(m.AttemptID)
	m.OTP = strings.TrimSpace(m.OTP)

	if m.DocType == "" {
		m.DocType = m.DocumentType
	} else if m.DocumentType != "" && m.DocType != m.DocumentType {
		return Message{}, fmt.Errorf("document_type does not match doc_type")
	}
	if m.DocumentType == "" {
		m.DocumentType = m.DocType
	}

	if m.ID == 0 {
		m.ID = m.SourceID
		if m.ID == 0 {
			m.ID = m.DocumentID
		}
	} else if m.SourceID != 0 && m.ID != m.SourceID {
		return Message{}, fmt.Errorf("source_id does not match id")
	}
	if m.SourceID == 0 {
		m.SourceID = m.ID
	}
	if m.DocumentID == 0 {
		m.DocumentID = m.SourceID
	} else if m.DocumentID != m.SourceID {
		return Message{}, fmt.Errorf("document_id does not match source_id")
	}

	if m.TenantID == "" {
		m.TenantID = m.DBName
	} else if m.DBName != "" && m.TenantID != m.DBName {
		return Message{}, fmt.Errorf("tenant_id does not match db_name")
	}
	if m.DBName == "" {
		m.DBName = m.TenantID
	}

	if m.Contract == "" {
		m.Contract = ContractName
	}
	if m.Version == "" {
		m.Version = ContractVersion
	}
	if m.SchemaVersion == "" {
		m.SchemaVersion = m.Version
	}
	if m.EGSID == "" && m.TenantID != "" && m.BranchID > 0 {
		m.EGSID = fmt.Sprintf("%s:branch:%d", m.TenantID, m.BranchID)
	}

	if m.StableDocumentKey == "" {
		m.StableDocumentKey = m.IdempotencyKey
	} else if m.IdempotencyKey != "" && m.StableDocumentKey != m.IdempotencyKey {
		return Message{}, fmt.Errorf("stable_document_key does not match idempotency_key")
	}
	if m.StableDocumentKey == "" && m.DocumentType != DocOnboard {
		m.StableDocumentKey = documentStableKey(m.TenantID, m.BranchID, m.DocumentType, m.SourceID)
	}
	if m.StableDocumentKey == "" && m.DocumentType == DocOnboard && m.OTP != "" {
		m.StableDocumentKey = onboardingStableKey(m.TenantID, m.BranchID, m.OTP)
	}
	if m.IdempotencyKey == "" {
		m.IdempotencyKey = m.StableDocumentKey
	}
	if m.EventID == "" && m.StableDocumentKey != "" {
		m.EventID = "evt-" + sha256Hex(m.StableDocumentKey)
	}
	if m.Attempt == 0 {
		m.Attempt = m.AttemptCount + 1
		if m.Attempt == 0 {
			m.Attempt = 1
		}
	}
	if m.AttemptID == "" {
		if m.OutboxID > 0 {
			m.AttemptID = fmt.Sprintf("%d:%d", m.OutboxID, m.Attempt)
		} else if m.StableDocumentKey != "" {
			m.AttemptID = fmt.Sprintf("%s:%d", m.StableDocumentKey, m.Attempt)
		}
	}
	return m, nil
}

// Validate checks the normalized message contract and identity invariants.
func (m Message) Validate() error {
	normalized, err := m.Normalize()
	if err != nil {
		return err
	}
	switch {
	case normalized.Contract != ContractName:
		return fmt.Errorf("unsupported contract %q", normalized.Contract)
	case normalized.Version != ContractVersion:
		return fmt.Errorf("unsupported contract version %q", normalized.Version)
	case normalized.SchemaVersion != ContractVersion:
		return fmt.Errorf("unsupported schema version %q", normalized.SchemaVersion)
	case normalized.TenantID == "":
		return fmt.Errorf("tenant_id is required")
	case normalized.BranchID <= 0:
		return fmt.Errorf("branch_id must be greater than zero")
	case !validDocType(normalized.DocumentType):
		return fmt.Errorf("unsupported document_type %q", normalized.DocumentType)
	case normalized.SourceID <= 0:
		return fmt.Errorf("source_id must be greater than zero")
	case normalized.StableDocumentKey == "":
		return fmt.Errorf("stable_document_key is required")
	case normalized.Attempt == 0:
		return fmt.Errorf("attempt must be greater than zero")
	case normalized.DocumentType == DocOnboard && normalized.OTP == "":
		return fmt.Errorf("otp is required for onboarding")
	case normalized.DocumentType != DocOnboard && normalized.OTP != "":
		return fmt.Errorf("otp is only valid for onboarding")
	}
	return nil
}

func validDocType(docType DocType) bool {
	switch docType {
	case DocBill, DocCredit, DocDebit, DocOnboard:
		return true
	default:
		return false
	}
}

// StableKey returns the durable logical identity used for deduplication.
func (m Message) StableKey() string {
	if key := strings.TrimSpace(m.StableDocumentKey); key != "" {
		return key
	}
	if key := strings.TrimSpace(m.IdempotencyKey); key != "" {
		return key
	}
	normalized, err := m.Normalize()
	if err == nil {
		return normalized.StableDocumentKey
	}
	if m.DocType == DocOnboard {
		return onboardingStableKey(m.DBName, m.BranchID, m.OTP)
	}
	return documentStableKey(m.DBName, m.BranchID, m.DocType, m.ID)
}

// NATSMessageID returns the stable identity for JetStream duplicate
// suppression. Attempt metadata is deliberately excluded.
func (m Message) NATSMessageID() string {
	return m.StableKey()
}

func documentStableKey(tenant string, branchID int64, docType DocType, sourceID int64) string {
	return fmt.Sprintf("%s:%d:%s:%d", strings.TrimSpace(tenant), branchID, docType, sourceID)
}

func onboardingStableKey(tenant string, branchID int64, otp string) string {
	return fmt.Sprintf(
		"%s:%d:%s:%d:otp-%s",
		strings.TrimSpace(tenant),
		branchID,
		DocOnboard,
		branchID,
		sha256Hex(strings.TrimSpace(otp))[:16],
	)
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
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
