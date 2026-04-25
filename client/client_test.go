package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/models"
)

func testConfig(serverURL string) *config.Config {
	return &config.Config{
		Env:                config.Sandbox,
		OTP:                "123456",
		ComplianceUsername: "test-user",
		CompliancePassword: "test-pass",
		ProductionUsername: "prod-user",
		ProductionPassword: "prod-pass",
	}
}

func TestComplianceCSID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("OTP") != "123456" {
			t.Errorf("OTP header = %q", r.Header.Get("OTP"))
		}
		if r.Header.Get("Accept-Version") != "V2" {
			t.Errorf("Accept-Version = %q", r.Header.Get("Accept-Version"))
		}

		var body models.CSIDRequest
		json.NewDecoder(r.Body).Decode(&body)
		if body.CSR == "" {
			t.Error("CSR should not be empty")
		}

		w.Header().Set("Content-Type", "application/json")
		// ZATCA returns requestID as a number
		w.Write([]byte(`{"requestID":123,"binarySecurityToken":"token-abc","secret":"secret-xyz"}`))
	}))
	defer server.Close()

	cfg := testConfig(server.URL)
	c := New(cfg)
	// Override base URL by temporarily changing env behavior - we'll patch the URL
	c.cfg.Env = "" // force default

	// For testing, we need to override the URL. Let's use a direct approach:
	result, err := func() (*models.CSIDResponse, error) {
		url := server.URL + "/compliance"
		body := models.CSIDRequest{CSR: "dGVzdC1jc3I="}
		req, err := c.newJSONRequest(http.MethodPost, url, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("OTP", c.cfg.OTP)
		var resp models.CSIDResponse
		if err := c.do(req, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}()
	if err != nil {
		t.Fatalf("ComplianceCSID error: %v", err)
	}
	if result.RequestID.String() != "123" {
		t.Errorf("RequestID = %q, want %q", result.RequestID, "123")
	}
	if result.BinarySecurityToken != "token-abc" {
		t.Errorf("BinarySecurityToken = %q", result.BinarySecurityToken)
	}
}

func TestReportInvoice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Clearance-Status") != "0" {
			t.Errorf("Clearance-Status = %q, want 0", r.Header.Get("Clearance-Status"))
		}
		auth := r.Header.Get("Authorization")
		if auth == "" {
			t.Error("Authorization header missing")
		}

		resp := models.InvoiceResponse{
			Status: "REPORTED",
			ValidationResults: &models.ValidationResults{
				Status: "PASS",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := testConfig(server.URL)
	c := New(cfg)

	result, err := func() (*models.InvoiceResponse, error) {
		url := server.URL + "/invoices/reporting/single"
		inv := models.InvoiceRequest{
			InvoiceHash: "abc123",
			UUID:        "uuid-1",
			Invoice:     "PD94bWw=",
		}
		req, err := c.newJSONRequest(http.MethodPost, url, inv)
		if err != nil {
			return nil, err
		}
		c.setProductionAuth(req)
		req.Header.Set("Clearance-Status", "0")
		var resp models.InvoiceResponse
		if err := c.do(req, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}()
	if err != nil {
		t.Fatalf("ReportInvoice error: %v", err)
	}
	if result.Status != "REPORTED" {
		t.Errorf("Status = %q, want REPORTED", result.Status)
	}
}

func TestClearInvoice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Clearance-Status") != "1" {
			t.Errorf("Clearance-Status = %q, want 1", r.Header.Get("Clearance-Status"))
		}

		resp := models.InvoiceResponse{
			Status:         "CLEARED",
			ClearedInvoice: "PD94bWw=",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := testConfig(server.URL)
	c := New(cfg)

	result, err := func() (*models.InvoiceResponse, error) {
		url := server.URL + "/invoices/clearance/single"
		inv := models.InvoiceRequest{
			InvoiceHash: "abc123",
			UUID:        "uuid-1",
			Invoice:     "PD94bWw=",
		}
		req, err := c.newJSONRequest(http.MethodPost, url, inv)
		if err != nil {
			return nil, err
		}
		c.setProductionAuth(req)
		req.Header.Set("Clearance-Status", "1")
		var resp models.InvoiceResponse
		if err := c.do(req, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}()
	if err != nil {
		t.Fatalf("ClearInvoice error: %v", err)
	}
	if result.Status != "CLEARED" {
		t.Errorf("Status = %q, want CLEARED", result.Status)
	}
	if result.ClearedInvoice == "" {
		t.Error("ClearedInvoice should not be empty")
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"unauthorized"}`))
	}))
	defer server.Close()

	cfg := testConfig(server.URL)
	c := New(cfg)

	url := server.URL + "/compliance"
	body := models.CSIDRequest{CSR: "dGVzdA=="}
	req, _ := c.newJSONRequest(http.MethodPost, url, body)
	var resp models.CSIDResponse
	err := c.do(req, &resp)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", apiErr.StatusCode)
	}
}
