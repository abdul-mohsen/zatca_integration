package models

import "encoding/json"

// CSIDRequest is the request body for Compliance/Production CSID API.
type CSIDRequest struct {
	CSR string `json:"csr"`
}

// CSIDResponse is the response from Compliance/Production CSID API.
type CSIDResponse struct {
	RequestID              json.Number `json:"requestID"`
	DispositionMessage     string      `json:"dispositionMessage"`
	BinarySecurityToken    string      `json:"binarySecurityToken"`
	Secret                 string      `json:"secret"`
	Errors                 []ErrorDetail `json:"errors,omitempty"`
}

// ProductionCSIDRequest is the request for Production CSID onboarding.
type ProductionCSIDRequest struct {
	ComplianceRequestID string `json:"compliance_request_id"`
}

// InvoiceRequest is the request body for Reporting/Clearance/Compliance APIs.
type InvoiceRequest struct {
	InvoiceHash string `json:"invoiceHash"`
	UUID        string `json:"uuid"`
	Invoice     string `json:"invoice"` // base64-encoded XML
}

// InvoiceResponse is the response from Reporting/Clearance/Compliance APIs.
type InvoiceResponse struct {
	InvoiceHash     string              `json:"invoiceHash,omitempty"`
	Status          string              `json:"status,omitempty"`
	ClearedInvoice  string              `json:"clearedInvoice,omitempty"` // base64 XML returned by clearance
	ValidationResults *ValidationResults `json:"validationResults,omitempty"`
	Warnings        []ErrorDetail       `json:"warnings,omitempty"`
	Errors          []ErrorDetail       `json:"errors,omitempty"`
}

// ValidationResults contains the detailed validation output.
type ValidationResults struct {
	InfoMessages    []ValidationMessage `json:"infoMessages,omitempty"`
	WarningMessages []ValidationMessage `json:"warningMessages,omitempty"`
	ErrorMessages   []ValidationMessage `json:"errorMessages,omitempty"`
	Status          string              `json:"status,omitempty"`
}

// ValidationMessage represents a single validation result.
type ValidationMessage struct {
	Type       string `json:"type,omitempty"`
	Code       string `json:"code,omitempty"`
	Category   string `json:"category,omitempty"`
	Message    string `json:"message,omitempty"`
	Status     string `json:"status,omitempty"`
}

// ErrorDetail represents an API error.
type ErrorDetail struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// RenewalRequest is the request for Production CSID renewal.
type RenewalRequest struct {
	CSR string `json:"csr"`
}
