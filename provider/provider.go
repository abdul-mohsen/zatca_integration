package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zatca-go/zatca/client"
	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/models"
)

const (
	KindStandard   = "standard"
	KindSimplified = "simplified"
)

// Submission is the immutable request sent for a committed invoice.
type Submission struct {
	Endpoint  string
	Kind      string
	Request   models.InvoiceRequest
	SignedXML string
}

func (s Submission) Validate() error {
	switch {
	case strings.TrimSpace(s.Endpoint) == "":
		return errors.New("provider endpoint is required")
	case s.Kind != KindStandard && s.Kind != KindSimplified:
		return fmt.Errorf("provider invoice kind %q is invalid", s.Kind)
	case strings.TrimSpace(s.Request.UUID) == "":
		return errors.New("provider invoice UUID is required")
	case strings.TrimSpace(s.Request.InvoiceHash) == "":
		return errors.New("provider invoice hash is required")
	case strings.TrimSpace(s.Request.Invoice) == "":
		return errors.New("provider invoice payload is required")
	case strings.TrimSpace(s.SignedXML) == "":
		return errors.New("provider signed XML is required")
	default:
		return nil
	}
}

// Adapter submits one committed invoice to a provider.
type Adapter interface {
	Submit(context.Context, Submission) (*models.InvoiceResponse, error)
}

// InvoiceClient is the invoice portion of the production HTTP client.
type InvoiceClient interface {
	ClearInvoice(models.InvoiceRequest) (*models.InvoiceResponse, error)
	ReportInvoice(models.InvoiceRequest) (*models.InvoiceResponse, error)
}

// ProductionAdapter preserves the existing ZATCA HTTP client behavior behind
// the provider seam.
type ProductionAdapter struct {
	client InvoiceClient
}

// NewProduction creates the default production adapter from the existing
// client. Credentials remain owned by config.Config and client.Client.
func NewProduction(c InvoiceClient) *ProductionAdapter {
	return &ProductionAdapter{client: c}
}

// NewProductionFromConfig creates the default adapter using the explicit
// configured endpoint and credentials.
func NewProductionFromConfig(cfg *config.Config) *ProductionAdapter {
	if cfg == nil {
		return NewProduction(nil)
	}
	return NewProduction(client.New(cfg))
}

func (p *ProductionAdapter) Submit(ctx context.Context, submission Submission) (*models.InvoiceResponse, error) {
	if err := submission.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.client == nil {
		return nil, errors.New("production provider client is not configured")
	}
	switch submission.Kind {
	case KindStandard:
		return p.client.ClearInvoice(submission.Request)
	case KindSimplified:
		return p.client.ReportInvoice(submission.Request)
	default:
		return nil, fmt.Errorf("provider invoice kind %q is invalid", submission.Kind)
	}
}

// ReconcileRequest identifies an already submitted invoice for external
// reconciliation.
type ReconcileRequest struct {
	Endpoint    string
	UUID        string
	InvoiceHash string
	SignedXML   string
}

// ReconcileResult is the provider answer obtained during reconciliation.
type ReconcileResult struct {
	State        string
	ProviderCode string
	Message      string
	EvidenceID   string
}

// Reconciler is optional; the production provider keeps the existing
// operator-supplied reconciliation path.
type Reconciler interface {
	Reconcile(context.Context, ReconcileRequest) (ReconcileResult, error)
}
