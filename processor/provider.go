package processor

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/google/uuid"

	"github.com/zatca-go/zatca/client"
	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/models"
	"github.com/zatca-go/zatca/zatca"
)

// OutcomeState is the durable state written to the T003 submission contract.
type OutcomeState string

const (
	OutcomePending             OutcomeState = "pending"
	OutcomeAccepted            OutcomeState = "accepted"
	OutcomeAcceptedWithWarning OutcomeState = "accepted_with_warning"
	OutcomeRejected            OutcomeState = "rejected"
	OutcomeAmbiguous           OutcomeState = "ambiguous"
	OutcomeTerminalFailure     OutcomeState = "terminal_failure"
)

func (s OutcomeState) valid() bool {
	switch s {
	case OutcomePending,
		OutcomeAccepted,
		OutcomeAcceptedWithWarning,
		OutcomeRejected,
		OutcomeAmbiguous,
		OutcomeTerminalFailure:
		return true
	default:
		return false
	}
}

func (s OutcomeState) success() bool {
	return s == OutcomeAccepted || s == OutcomeAcceptedWithWarning
}

// SubmissionProvider is the narrow SDK/provider boundary used by the
// processor. The production implementation wraps the existing ZATCA service;
// tests can supply a deterministic fake without Docker or HTTP.
type SubmissionProvider interface {
	SignInvoice(xmlContent string) (signedXML string, hash string, err error)
	GenerateAPIRequest(signedXML string) (models.InvoiceRequest, error)
	GenerateQR(signedXML string) (string, error)
	Submit(request models.InvoiceRequest, standard bool) (*ProviderResult, error)
}

// ProviderResult is the normalized provider response consumed by the
// processor. Empty optional values remain empty and are persisted as NULL.
type ProviderResult struct {
	State                OutcomeState
	ProviderCode         string
	WarningCode          string
	WarningMessage       string
	ProviderRef          string
	RejectionCode        string
	RejectionMessage     string
	InvoiceHash          string
	ClearedXML           string
	ResponseXML          string
	ProviderResponse     string
	ProviderResponseType string
}

// ProviderError describes a provider failure with an explicit durable state.
type ProviderError struct {
	State   OutcomeState
	Code    string
	Message string
	Err     error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.State)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type serviceSubmissionProvider struct {
	svc *zatca.Service
}

func newServiceSubmissionProvider(svc *zatca.Service) SubmissionProvider {
	return serviceSubmissionProvider{svc: svc}
}

func (p serviceSubmissionProvider) SignInvoice(xmlContent string) (string, string, error) {
	if p.svc == nil || p.svc.SDK == nil {
		return "", "", fmt.Errorf("ZATCA SDK is not configured")
	}
	return p.svc.SDK.SignInvoice(xmlContent)
}

func (p serviceSubmissionProvider) GenerateAPIRequest(signedXML string) (models.InvoiceRequest, error) {
	if p.svc == nil || p.svc.SDK == nil {
		return models.InvoiceRequest{}, fmt.Errorf("ZATCA SDK is not configured")
	}
	apiJSON, err := p.svc.SDK.GenerateAPIRequest(signedXML)
	if err != nil {
		return models.InvoiceRequest{}, err
	}
	var req models.InvoiceRequest
	if err := json.Unmarshal([]byte(apiJSON), &req); err != nil {
		return models.InvoiceRequest{}, fmt.Errorf("parse API request: %w", err)
	}
	return req, nil
}

func (p serviceSubmissionProvider) GenerateQR(signedXML string) (string, error) {
	if p.svc == nil || p.svc.SDK == nil {
		return "", fmt.Errorf("ZATCA SDK is not configured")
	}
	return p.svc.SDK.GenerateQR(signedXML)
}

func (p serviceSubmissionProvider) Submit(request models.InvoiceRequest, standard bool) (*ProviderResult, error) {
	if p.svc == nil || p.svc.Client == nil {
		return nil, &ProviderError{
			State:   OutcomeTerminalFailure,
			Code:    "PROVIDER_NOT_CONFIGURED",
			Message: "ZATCA provider is not configured",
		}
	}

	var (
		resp *models.InvoiceResponse
		err  error
	)
	if standard {
		resp, err = p.svc.Client.ClearInvoice(request)
	} else {
		resp, err = p.svc.Client.ReportInvoice(request)
	}
	if err != nil {
		return nil, classifyProviderError(err)
	}
	if resp == nil {
		return nil, &ProviderError{
			State:   OutcomeTerminalFailure,
			Code:    "NIL_PROVIDER_RESPONSE",
			Message: "ZATCA provider returned a nil response",
		}
	}
	return normalizeProviderResponse(resp), nil
}

func normalizeProviderResponse(resp *models.InvoiceResponse) *ProviderResult {
	result := &ProviderResult{
		State:        OutcomePending,
		ProviderCode: strings.TrimSpace(resp.Status),
		InvoiceHash:  strings.TrimSpace(resp.InvoiceHash),
	}
	if response, err := json.Marshal(resp); err == nil {
		result.ProviderResponse = string(response)
		result.ProviderResponseType = "application/json"
	}

	if len(resp.Errors) > 0 {
		result.State = OutcomeRejected
		result.RejectionCode = strings.TrimSpace(resp.Errors[0].Code)
		result.RejectionMessage = strings.TrimSpace(resp.Errors[0].Message)
		if result.ProviderCode == "" {
			result.ProviderCode = result.RejectionCode
		}
	} else {
		switch strings.ToUpper(strings.TrimSpace(resp.Status)) {
		case "REPORTED", "CLEARED", "ACCEPTED", "SUCCESS", "PASS":
			result.State = OutcomeAccepted
		case "REJECTED", "ERROR", "FAILED", "FAILURE":
			result.State = OutcomeRejected
		}
		if result.State == OutcomePending &&
			resp.ValidationResults != nil &&
			strings.EqualFold(strings.TrimSpace(resp.ValidationResults.Status), "PASS") {
			result.State = OutcomeAccepted
		}
	}

	if code, message := firstWarning(resp); code != "" || message != "" {
		result.WarningCode = code
		result.WarningMessage = message
		if result.State == OutcomeAccepted {
			result.State = OutcomeAcceptedWithWarning
		}
	}

	if resp.ClearedInvoice != "" {
		decoded, err := base64.StdEncoding.DecodeString(resp.ClearedInvoice)
		if err != nil {
			return &ProviderResult{
				State:            OutcomeTerminalFailure,
				ProviderCode:     "INVALID_CLEARED_XML",
				RejectionMessage: err.Error(),
			}
		}
		result.ClearedXML = string(decoded)
		result.ResponseXML = result.ClearedXML
	}
	return result
}

func firstWarning(resp *models.InvoiceResponse) (string, string) {
	if len(resp.Warnings) > 0 {
		return strings.TrimSpace(resp.Warnings[0].Code), strings.TrimSpace(resp.Warnings[0].Message)
	}
	if resp.ValidationResults != nil && len(resp.ValidationResults.WarningMessages) > 0 {
		w := resp.ValidationResults.WarningMessages[0]
		return strings.TrimSpace(w.Code), strings.TrimSpace(w.Message)
	}
	return "", ""
}

func classifyProviderError(err error) error {
	if err == nil {
		return nil
	}

	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		state := OutcomeRejected
		if apiErr.StatusCode == 408 || apiErr.StatusCode == 409 || apiErr.StatusCode == 429 || apiErr.StatusCode >= 500 {
			state = OutcomeAmbiguous
		}
		return &ProviderError{
			State:   state,
			Code:    fmt.Sprintf("HTTP_%d", apiErr.StatusCode),
			Message: apiErr.Error(),
			Err:     err,
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &ProviderError{
			State:   OutcomeAmbiguous,
			Code:    "TIMEOUT",
			Message: err.Error(),
			Err:     err,
		}
	}

	return &ProviderError{
		State:   OutcomeAmbiguous,
		Code:    "TRANSPORT_ERROR",
		Message: err.Error(),
		Err:     err,
	}
}

func stableInvoiceUUID(cfg *config.Config, documentKind string, sourceID int64) string {
	tenant := cfg.TenantID
	if tenant == "" {
		tenant = fmt.Sprintf("merchant:%d", cfg.MerchantID)
	}
	seed := fmt.Sprintf("%s:%s:%d", tenant, documentKind, sourceID)
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
}
