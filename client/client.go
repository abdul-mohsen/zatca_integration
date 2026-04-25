package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/models"
)

// Client is the ZATCA API client.
type Client struct {
	cfg        *config.Config
	httpClient *http.Client
}

// New creates a new ZATCA API client from config.
func New(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// --- Compliance CSID (Onboarding) ---

// ComplianceCSID requests a compliance CSID from ZATCA.
// The csr should be the base64-encoded PEM content (entire PEM including headers).
func (c *Client) ComplianceCSID(csr string) (*models.CSIDResponse, error) {
	url := c.cfg.BaseURL() + "/compliance"
	body := models.CSIDRequest{CSR: csr}

	req, err := c.newJSONRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header["OTP"] = []string{c.cfg.OTP}
	// No basic auth for compliance CSID — only OTP

	var resp models.CSIDResponse
	if err := c.do(req, &resp); err != nil {
		return nil, fmt.Errorf("ComplianceCSID: %w", err)
	}
	return &resp, nil
}

// --- Compliance Invoice Check ---

// ComplianceCheck submits an invoice for compliance validation.
func (c *Client) ComplianceCheck(invoice models.InvoiceRequest) (*models.InvoiceResponse, error) {
	url := c.cfg.BaseURL() + "/compliance/invoices"

	req, err := c.newJSONRequest(http.MethodPost, url, invoice)
	if err != nil {
		return nil, err
	}
	c.setComplianceAuth(req)

	var resp models.InvoiceResponse
	if err := c.do(req, &resp); err != nil {
		return nil, fmt.Errorf("ComplianceCheck: %w", err)
	}
	return &resp, nil
}

// --- Production CSID (Onboarding) ---

// ProductionCSID requests a production CSID after passing compliance.
func (c *Client) ProductionCSID(complianceRequestID string) (*models.CSIDResponse, error) {
	url := c.cfg.BaseURL() + "/production/csids"
	body := models.ProductionCSIDRequest{ComplianceRequestID: complianceRequestID}

	req, err := c.newJSONRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	c.setComplianceAuth(req)

	var resp models.CSIDResponse
	if err := c.do(req, &resp); err != nil {
		return nil, fmt.Errorf("ProductionCSID: %w", err)
	}
	return &resp, nil
}

// --- Production CSID (Renewal) ---

// RenewProductionCSID renews a production CSID.
// The csr should be the base64-encoded PEM content (entire PEM including headers).
func (c *Client) RenewProductionCSID(csr string) (*models.CSIDResponse, error) {
	url := c.cfg.BaseURL() + "/production/csids"
	body := models.RenewalRequest{CSR: csr}

	req, err := c.newJSONRequest(http.MethodPatch, url, body)
	if err != nil {
		return nil, err
	}
	c.setProductionAuth(req)
	req.Header["OTP"] = []string{c.cfg.OTP}

	var resp models.CSIDResponse
	if err := c.do(req, &resp); err != nil {
		return nil, fmt.Errorf("RenewProductionCSID: %w", err)
	}
	return &resp, nil
}

// --- Reporting API ---

// ReportInvoice reports a simplified invoice (B2C).
func (c *Client) ReportInvoice(invoice models.InvoiceRequest) (*models.InvoiceResponse, error) {
	url := c.cfg.BaseURL() + "/invoices/reporting/single"

	req, err := c.newJSONRequest(http.MethodPost, url, invoice)
	if err != nil {
		return nil, err
	}
	c.setProductionAuth(req)
	req.Header.Set("Clearance-Status", "0")

	// Debug: report whether Authorization header was set (masked)
	if auth := req.Header.Get("Authorization"); auth == "" {
		log.Printf("DEBUG: ReportInvoice: Authorization header not set")
	} else {
		masked := auth
		if len(masked) > 12 {
			masked = masked[:12] + "..."
		}
		log.Printf("DEBUG: ReportInvoice: Authorization header set (len=%d masked=%s)", len(auth), masked)
	}

	var resp models.InvoiceResponse
	if err := c.do(req, &resp); err != nil {
		return nil, fmt.Errorf("ReportInvoice: %w", err)
	}
	return &resp, nil
}

// --- Clearance API ---

// ClearInvoice clears a standard invoice (B2B).
func (c *Client) ClearInvoice(invoice models.InvoiceRequest) (*models.InvoiceResponse, error) {
	url := c.cfg.BaseURL() + "/invoices/clearance/single"

	req, err := c.newJSONRequest(http.MethodPost, url, invoice)
	if err != nil {
		return nil, err
	}
	c.setProductionAuth(req)
	req.Header.Set("Clearance-Status", "1")

	var resp models.InvoiceResponse
	if err := c.do(req, &resp); err != nil {
		return nil, fmt.Errorf("ClearInvoice: %w", err)
	}
	return &resp, nil
}

// --- Helpers ---

func (c *Client) newJSONRequest(method, url string, body any) (*http.Request, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, fmt.Errorf("encoding request body: %w", err)
	}

	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Version", "V2")
	req.Header.Set("Accept-Language", "en")
	// Debug: log outgoing URL and environment
	if c != nil && c.cfg != nil {
		log.Printf("DEBUG: outgoing request method=%s url=%s env=%s", method, url, c.cfg.Env)
	} else {
		log.Printf("DEBUG: outgoing request method=%s url=%s", method, url)
	}
	return req, nil
}

func (c *Client) setComplianceAuth(req *http.Request) {
	userPresent := c.cfg.ComplianceUsername != ""
	passPresent := c.cfg.CompliancePassword != ""
	if userPresent && passPresent {
		token := base64.StdEncoding.EncodeToString(
			[]byte(c.cfg.ComplianceUsername + ":" + c.cfg.CompliancePassword),
		)
		req.Header.Set("Authorization", "Basic "+token)
		log.Printf("DEBUG: setComplianceAuth: Authorization header set (compliance)")
	} else {
		log.Printf("DEBUG: setComplianceAuth: credentials missing (username present=%t password present=%t)", userPresent, passPresent)
	}
}

func (c *Client) setProductionAuth(req *http.Request) {
	userPresent := c.cfg.ProductionUsername != ""
	passPresent := c.cfg.ProductionPassword != ""
	if userPresent && passPresent {
		token := base64.StdEncoding.EncodeToString(
			[]byte(c.cfg.ProductionUsername + ":" + c.cfg.ProductionPassword),
		)
		req.Header.Set("Authorization", "Basic "+token)
		log.Printf("DEBUG: setProductionAuth: Authorization header set (production)")
	} else {
		log.Printf("DEBUG: setProductionAuth: credentials missing (username present=%t password present=%t)", userPresent, passPresent)
	}
}

// APIError is returned when the API responds with a non-2xx status.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ZATCA API error (HTTP %d): %s", e.StatusCode, e.Body)
}

func (c *Client) do(req *http.Request, result any) error {
	// Debug: log whether Authorization header is present before sending (masked)
	if auth := req.Header.Get("Authorization"); auth == "" {
		log.Printf("DEBUG: do: Authorization header missing")
	} else {
		masked := auth
		if len(masked) > 12 {
			masked = masked[:12] + "..."
		}
		log.Printf("DEBUG: do: Authorization header present (len=%d masked=%s)", len(auth), masked)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("DEBUG: do: non-2xx response status=%d body=%s", resp.StatusCode, string(bodyBytes))
		return &APIError{StatusCode: resp.StatusCode, Body: string(bodyBytes)}
	}

	if result != nil && len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}
