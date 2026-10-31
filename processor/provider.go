package processor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zatca-go/zatca/client"
	"github.com/zatca-go/zatca/models"
	"github.com/zatca-go/zatca/zatca"
)

var ErrProviderLookupUnavailable = errors.New("provider lookup is not configured")

// serviceSubmissionProvider adapts the concrete HTTP client to the narrow
// provider boundary. The lookup method is deliberately explicit: until a
// provider lookup endpoint is configured, ambiguous attempts remain manual
// review rather than being blindly resubmitted.
type serviceSubmissionProvider struct {
	service *zatca.Service
}

func newServiceSubmissionProvider(service *zatca.Service) SubmissionProvider {
	return &serviceSubmissionProvider{service: service}
}

func (p *serviceSubmissionProvider) Submit(_ context.Context, request SubmissionRequest) (*ProviderResponse, error) {
	if p == nil || p.service == nil || p.service.Client == nil {
		return nil, errors.New("ZATCA provider client is not configured")
	}
	var apiRequest models.InvoiceRequest
	if err := json.Unmarshal(request.ProviderRequest, &apiRequest); err != nil {
		return nil, fmt.Errorf("decode provider request artifact: %w", err)
	}

	var response *models.InvoiceResponse
	var err error
	if request.Standard {
		response, err = p.service.Client.ClearInvoice(apiRequest)
	} else {
		response, err = p.service.Client.ReportInvoice(apiRequest)
	}
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			return nil, &ProviderHTTPError{
				StatusCode: apiErr.StatusCode,
				Body:       []byte(apiErr.Body),
				Err:        err,
			}
		}
		return nil, err
	}
	if response == nil {
		return nil, nil
	}
	raw, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return nil, fmt.Errorf("encode provider response artifact: %w", marshalErr)
	}
	result := &ProviderResponse{
		Found:            true,
		HTTPStatus:       200,
		Status:           response.Status,
		InvoiceHash:      response.InvoiceHash,
		ResponseArtifact: raw,
		Warnings:         append([]models.ErrorDetail(nil), response.Warnings...),
		Errors:           append([]models.ErrorDetail(nil), response.Errors...),
	}
	if response.ClearedInvoice != "" {
		if cleared, decodeErr := base64.StdEncoding.DecodeString(response.ClearedInvoice); decodeErr == nil {
			result.ClearedArtifact = cleared
		}
	}
	return result, nil
}

func (p *serviceSubmissionProvider) Lookup(context.Context, SubmissionIdentity) (*ProviderResponse, error) {
	return nil, ErrProviderLookupUnavailable
}
