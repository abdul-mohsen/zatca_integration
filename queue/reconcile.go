package queue

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	ReconcileActionStatus = "status"
	ReconcileActionRecord = "record"
)

// ReconcileRequest is the operator boundary for an already committed invoice.
// A record request must carry the exact stored invoice hash and an external
// evidence identifier. The daemon never turns a missing provider answer into
// an accepted or rejected result.
type ReconcileRequest struct {
	TenantKey    string `json:"tenant_key"`
	DBName       string `json:"db_name"`
	BranchID     int64  `json:"branch_id"`
	BillID       int64  `json:"bill_id"`
	Action       string `json:"action"`
	InvoiceUUID  string `json:"invoice_uuid,omitempty"`
	InvoiceHash  string `json:"invoice_hash,omitempty"`
	State        string `json:"state,omitempty"`
	ProviderCode string `json:"provider_code,omitempty"`
	Message      string `json:"message,omitempty"`
	EvidenceID   string `json:"evidence_id,omitempty"`
}

// ReconcileReply is intentionally descriptive rather than a provider result.
// Evidence is "recorded" for lookup and "external" only after a supplied
// external answer has been validated and persisted.
type ReconcileReply struct {
	Accepted    bool   `json:"accepted"`
	State       string `json:"state,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	EvidenceID  string `json:"evidence_id,omitempty"`
	InvoiceHash string `json:"invoice_hash,omitempty"`
	Error       string `json:"error,omitempty"`
}

func ReconcileSubject(tenantKey string) string {
	return fmt.Sprintf("control.zatca.reconcile.%s", tenantKey)
}

func ReconcileWildcard() string {
	return "control.zatca.reconcile.*"
}

func DecodeReconcileRequest(data []byte) (ReconcileRequest, error) {
	var req ReconcileRequest
	if err := strictDecode(data, &req); err != nil {
		return ReconcileRequest{}, err
	}
	if err := req.Validate(); err != nil {
		return ReconcileRequest{}, err
	}
	return req, nil
}

func (r ReconcileRequest) Validate() error {
	switch {
	case !validIdentityText(r.TenantKey, 64):
		return invalidf("tenant_key is required and must be at most 64 characters")
	case !validIdentityText(r.DBName, 64):
		return invalidf("db_name is required and must be at most 64 characters")
	case r.BranchID <= 0:
		return invalidf("branch_id is required")
	case r.BillID <= 0:
		return invalidf("bill_id is required")
	case r.Action != ReconcileActionStatus && r.Action != ReconcileActionRecord:
		return invalidf("action must be %q or %q", ReconcileActionStatus, ReconcileActionRecord)
	}
	if r.InvoiceUUID != "" && !isUUID(r.InvoiceUUID) {
		return invalidf("invoice_uuid must be a UUID when supplied")
	}
	if r.InvoiceHash != "" && !validFingerprint(r.InvoiceHash) {
		return invalidf("invoice_hash must be a 64-character SHA-256 hex value")
	}
	if r.Action != ReconcileActionRecord {
		return nil
	}
	switch r.State {
	case OutcomeAccepted, OutcomeWarning, OutcomeRejected:
	default:
		return invalidf("record state must be accepted, accepted_with_warning, or rejected")
	}
	if strings.TrimSpace(r.InvoiceHash) == "" {
		return invalidf("invoice_hash is required when recording external evidence")
	}
	if strings.TrimSpace(r.EvidenceID) == "" || !validEvidenceText(r.EvidenceID, 256) {
		return invalidf("evidence_id is required and must be at most 256 characters")
	}
	if !validEvidenceText(r.ProviderCode, 64) || !validEvidenceText(r.Message, 1024) {
		return invalidf("provider_code and message must be valid bounded text")
	}
	return nil
}

func validEvidenceText(value string, maxRunes int) bool {
	if utf8.RuneCountInString(value) > maxRunes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == 0 || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func EncodeReconcileRequest(req ReconcileRequest) ([]byte, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(req)
}
