package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/zatca-go/zatca/client"
	"github.com/zatca-go/zatca/models"
)

// Scenario is a deterministic provider behavior used by integration tests.
type Scenario string

const (
	ScenarioAccepted             Scenario = "accepted"
	ScenarioWarning              Scenario = "warning"
	ScenarioRejected             Scenario = "rejected"
	ScenarioTimeout              Scenario = "timeout"
	ScenarioTechnical            Scenario = "technical"
	ScenarioConflict             Scenario = "conflict"
	ScenarioCrashAfterAcceptance Scenario = "crash_after_acceptance"
	ScenarioReconcileAccepted    Scenario = "reconcile_accepted"
	ScenarioReconcileWarning     Scenario = "reconcile_warning"
	ScenarioReconcileRejected    Scenario = "reconcile_rejected"
	ScenarioReconcileTimeout     Scenario = "reconcile_timeout"
	ScenarioReconcileTechnical   Scenario = "reconcile_technical"
)

// LocalOptions configures a deterministic provider without credentials or
// network access.
type LocalOptions struct {
	SubmitScenario      Scenario
	ReconcileScenario   Scenario
	WarningCode         string
	WarningMessage      string
	RejectionCode       string
	RejectionMessage    string
	ConflictMessage     string
	TechnicalMessage    string
	TimeoutMessage      string
	ReconcileEvidenceID string
}

// Local is a deterministic, concurrency-safe provider adapter for tests.
type Local struct {
	mu              sync.Mutex
	options         LocalOptions
	submissions     []Submission
	reconciliations []ReconcileRequest
	submitCalls     map[string]int
}

// NewLocal creates a credential-free local provider. An empty submit scenario
// defaults to acceptance.
func NewLocal(options LocalOptions) *Local {
	if options.SubmitScenario == "" {
		options.SubmitScenario = ScenarioAccepted
	}
	if options.WarningCode == "" {
		options.WarningCode = "LOCAL-WARN"
	}
	if options.WarningMessage == "" {
		options.WarningMessage = "local provider warning"
	}
	if options.RejectionCode == "" {
		options.RejectionCode = "LOCAL-REJECT"
	}
	if options.RejectionMessage == "" {
		options.RejectionMessage = "local provider rejection"
	}
	if options.ConflictMessage == "" {
		options.ConflictMessage = "local provider already accepted the invoice"
	}
	if options.TechnicalMessage == "" {
		options.TechnicalMessage = "local provider technical failure"
	}
	if options.TimeoutMessage == "" {
		options.TimeoutMessage = "local provider timeout"
	}
	if options.ReconcileEvidenceID == "" {
		options.ReconcileEvidenceID = "local-reconciliation"
	}
	return &Local{
		options:     options,
		submitCalls: make(map[string]int),
	}
}

func (p *Local) Submit(ctx context.Context, submission Submission) (*models.InvoiceResponse, error) {
	if err := submission.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.submissions = append(p.submissions, submission)
	p.submitCalls[submission.Request.UUID]++
	call := p.submitCalls[submission.Request.UUID]
	options := p.options
	p.mu.Unlock()

	switch options.SubmitScenario {
	case ScenarioAccepted:
		return acceptedResponse(submission), nil
	case ScenarioWarning:
		resp := acceptedResponse(submission)
		resp.ValidationResults = &models.ValidationResults{
			Status: "PASS",
			WarningMessages: []models.ValidationMessage{{
				Code:    options.WarningCode,
				Message: options.WarningMessage,
			}},
		}
		return resp, nil
	case ScenarioRejected:
		resp := acceptedResponse(submission)
		resp.ValidationResults = &models.ValidationResults{
			Status: "ERROR",
			ErrorMessages: []models.ValidationMessage{{
				Code:    options.RejectionCode,
				Message: options.RejectionMessage,
			}},
		}
		return resp, nil
	case ScenarioTimeout:
		return nil, &client.APIError{StatusCode: http.StatusRequestTimeout, Body: options.TimeoutMessage}
	case ScenarioTechnical:
		return nil, errors.New(options.TechnicalMessage)
	case ScenarioConflict:
		return nil, &client.APIError{StatusCode: http.StatusConflict, Body: options.ConflictMessage}
	case ScenarioCrashAfterAcceptance:
		if call == 1 {
			return nil, errors.New("local provider crashed after acceptance")
		}
		return acceptedResponse(submission), nil
	default:
		return nil, fmt.Errorf("unsupported local submit scenario %q", options.SubmitScenario)
	}
}

func (p *Local) Reconcile(ctx context.Context, request ReconcileRequest) (ReconcileResult, error) {
	if err := ctx.Err(); err != nil {
		return ReconcileResult{}, err
	}
	p.mu.Lock()
	p.reconciliations = append(p.reconciliations, request)
	options := p.options
	p.mu.Unlock()

	result := ReconcileResult{
		EvidenceID: options.ReconcileEvidenceID,
	}
	switch options.ReconcileScenario {
	case ScenarioReconcileAccepted:
		result.State = "accepted"
		result.ProviderCode = "LOCAL-CLEARED"
		result.Message = "local reconciliation accepted"
	case ScenarioReconcileWarning:
		result.State = "accepted_with_warning"
		result.ProviderCode = "LOCAL-WARN"
		result.Message = "local reconciliation warning"
	case ScenarioReconcileRejected:
		result.State = "rejected"
		result.ProviderCode = "LOCAL-REJECT"
		result.Message = "local reconciliation rejected"
	case ScenarioReconcileTimeout:
		return ReconcileResult{}, &client.APIError{StatusCode: http.StatusRequestTimeout, Body: options.TimeoutMessage}
	case ScenarioReconcileTechnical:
		return ReconcileResult{}, errors.New(options.TechnicalMessage)
	default:
		return ReconcileResult{}, fmt.Errorf("unsupported local reconcile scenario %q", options.ReconcileScenario)
	}
	return result, nil
}

func acceptedResponse(submission Submission) *models.InvoiceResponse {
	resp := &models.InvoiceResponse{InvoiceHash: submission.Request.InvoiceHash}
	if submission.Kind == KindStandard {
		resp.ClearanceStatus = "CLEARED"
	} else {
		resp.ReportingStatus = "REPORTED"
	}
	return resp
}

// Submissions returns a snapshot of requests received by the local provider.
func (p *Local) Submissions() []Submission {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Submission, len(p.submissions))
	copy(out, p.submissions)
	return out
}

// Reconciliations returns a snapshot of reconciliation requests received.
func (p *Local) Reconciliations() []ReconcileRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ReconcileRequest, len(p.reconciliations))
	copy(out, p.reconciliations)
	return out
}

// SetSubmitScenario changes the deterministic submit behavior for subsequent
// calls.
func (p *Local) SetSubmitScenario(scenario Scenario) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.options.SubmitScenario = scenario
}

// SetReconcileScenario changes the deterministic reconciliation behavior.
func (p *Local) SetReconcileScenario(scenario Scenario) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.options.ReconcileScenario = scenario
}
