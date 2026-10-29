package processor

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/models"
)

type fakeSubmissionProvider struct {
	result *ProviderResult
	err    error
}

func (f fakeSubmissionProvider) SignInvoice(string) (string, string, error) {
	return "<signed-invoice/>", "hash-41", nil
}

func (f fakeSubmissionProvider) GenerateAPIRequest(string) (models.InvoiceRequest, error) {
	return models.InvoiceRequest{
		InvoiceHash: "hash-41",
		UUID:        "00000000-0000-0000-0000-000000000041",
		Invoice:     "c2lnbmVkLWludm9pY2U=",
	}, nil
}

func (f fakeSubmissionProvider) GenerateQR(string) (string, error) {
	return "qr-41", nil
}

func (f fakeSubmissionProvider) Submit(models.InvoiceRequest, bool) (*ProviderResult, error) {
	return f.result, f.err
}

type fakeSubmissionStore struct {
	attempt submissionAttempt
	outcome submissionOutcome
	update  documentUpdate
	arts    []artifactRecord
}

func (f *fakeSubmissionStore) StartSubmission(submissionMeta, string, time.Time) (submissionAttempt, error) {
	if f.attempt.attemptNumber == 0 {
		f.attempt = submissionAttempt{
			submissionID:  10,
			attemptID:     20,
			attemptNumber: 1,
			startedAt:     time.Now(),
		}
	}
	return f.attempt, nil
}

func (f *fakeSubmissionStore) CompleteSubmission(
	_ submissionAttempt,
	outcome submissionOutcome,
	update documentUpdate,
	arts []artifactRecord,
) error {
	f.outcome = outcome
	f.update = update
	f.arts = arts
	return nil
}

func TestSignSubmitUpdateWithProviderOutcomes(t *testing.T) {
	defer os.RemoveAll("processor_test_artifacts")

	tests := []struct {
		name        string
		result      *ProviderResult
		providerErr error
		wantState   OutcomeState
		wantIssued  bool
		wantErr     bool
	}{
		{
			name: "accepted",
			result: &ProviderResult{
				State:        OutcomeAccepted,
				ProviderCode: "CLEARED",
				ProviderRef:  "zatca-ref-41",
				ClearedXML:   "<cleared-invoice/>",
			},
			wantState:  OutcomeAccepted,
			wantIssued: true,
		},
		{
			name: "accepted with warning",
			result: &ProviderResult{
				State:          OutcomeAcceptedWithWarning,
				ProviderCode:   "REPORTED",
				WarningCode:    "W-UBL-01",
				WarningMessage: "minor validation warning",
				ProviderRef:    "zatca-ref-41",
			},
			wantState:  OutcomeAcceptedWithWarning,
			wantIssued: true,
		},
		{
			name: "rejected",
			result: &ProviderResult{
				State:            OutcomeRejected,
				ProviderCode:     "REJECTED",
				RejectionCode:    "BR-KSA-01",
				RejectionMessage: "invoice rejected",
			},
			wantState: OutcomeRejected,
			wantErr:   true,
		},
		{
			name:        "timeout is ambiguous",
			providerErr: &ProviderError{State: OutcomeAmbiguous, Code: "TIMEOUT", Message: "provider timeout"},
			wantState:   OutcomeAmbiguous,
			wantErr:     true,
		},
		{
			name:      "pending response",
			result:    &ProviderResult{State: OutcomePending, ProviderCode: "PENDING"},
			wantState: OutcomePending,
			wantErr:   true,
		},
		{
			name:      "nil response is terminal",
			result:    nil,
			wantState: OutcomeTerminalFailure,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeSubmissionStore{}
			provider := fakeSubmissionProvider{result: tt.result, err: tt.providerErr}
			inv := outcomeTestInvoice()
			meta := xmlMeta{
				xmlDir:       "processor_test_artifacts",
				tenantID:     "tenant-a",
				merchantID:   7,
				sourceBillID: 41,
				companyID:    3,
				branchID:     2,
				billType:     "invoice",
				table:        "bill",
				qrColumn:     "qr_code",
				documentKind: "bill",
			}

			_, err := signSubmitUpdateWithProvider(provider, store, inv, 41, meta)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, tt.wantErr)
			}
			if store.outcome.State != tt.wantState {
				t.Fatalf("outcome state = %q, want %q", store.outcome.State, tt.wantState)
			}
			if store.update.MarkIssued != tt.wantIssued {
				t.Fatalf("mark issued = %t, want %t", store.update.MarkIssued, tt.wantIssued)
			}
			if store.update.MerchantID != 7 {
				t.Fatalf("merchant id = %d, want 7", store.update.MerchantID)
			}
			if store.update.InvoiceUUID != inv.UUID {
				t.Fatalf("invoice UUID = %q, want %q", store.update.InvoiceUUID, inv.UUID)
			}
			if store.update.InvoiceHash != "hash-41" {
				t.Fatalf("invoice hash = %q, want hash-41", store.update.InvoiceHash)
			}
			if store.update.QR != "qr-41" {
				t.Fatalf("QR = %q, want qr-41", store.update.QR)
			}
			if store.attempt.attemptNumber != 1 {
				t.Fatalf("attempt number = %d, want 1", store.attempt.attemptNumber)
			}
			if len(store.arts) == 0 {
				t.Fatal("expected request XML artifact")
			}
			if !strings.Contains(store.outcome.RequestXMLPath, "tenant-a") {
				t.Fatalf("request path = %q, want tenant context", store.outcome.RequestXMLPath)
			}
		})
	}
}

func TestSignSubmitUpdateWithProviderPreservesProviderWarning(t *testing.T) {
	defer os.RemoveAll("processor_test_artifacts")

	store := &fakeSubmissionStore{}
	provider := fakeSubmissionProvider{
		result: &ProviderResult{
			State:          OutcomeAcceptedWithWarning,
			ProviderCode:   "REPORTED",
			WarningCode:    "W-UBL-01",
			WarningMessage: "warning text",
		},
	}
	inv := outcomeTestInvoice()
	_, err := signSubmitUpdateWithProvider(provider, store, inv, 41, outcomeTestMeta())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.outcome.ProviderCode != "REPORTED" {
		t.Fatalf("provider code = %q, want REPORTED", store.outcome.ProviderCode)
	}
	if store.outcome.WarningCode != "W-UBL-01" {
		t.Fatalf("warning code = %q, want W-UBL-01", store.outcome.WarningCode)
	}
	if store.outcome.WarningMessage != "warning text" {
		t.Fatalf("warning message = %q, want warning text", store.outcome.WarningMessage)
	}
}

func TestStandardClearanceRetainsAuthoritativeClearedXML(t *testing.T) {
	defer os.RemoveAll("processor_test_artifacts")

	const clearedXML = `<Invoice><cbc:UUID>cleared-41</cbc:UUID><cbc:QR>stamped</cbc:QR></Invoice>`
	store := &fakeSubmissionStore{}
	provider := fakeSubmissionProvider{
		result: &ProviderResult{
			State:        OutcomeAccepted,
			ProviderCode: "CLEARED",
			ProviderRef:  "clearance-ref-41",
			ClearedXML:   clearedXML,
		},
	}

	if _, err := signSubmitUpdateWithProvider(provider, store, outcomeTestInvoice(), 41, outcomeTestMeta()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.outcome.ResponseXML != clearedXML {
		t.Fatalf("response XML = %q, want authoritative cleared XML %q", store.outcome.ResponseXML, clearedXML)
	}
	if store.outcome.ClearedXMLPath == "" {
		t.Fatal("cleared XML path is empty")
	}
	if store.outcome.ProviderRef != "clearance-ref-41" {
		t.Fatalf("provider reference = %q, want clearance-ref-41", store.outcome.ProviderRef)
	}

	var clearedArtifact artifactRecord
	for _, artifact := range store.arts {
		if artifact.Kind == "cleared_xml" {
			clearedArtifact = artifact
			break
		}
	}
	if clearedArtifact.Path == "" {
		t.Fatal("cleared XML artifact was not recorded")
	}
	got, err := os.ReadFile(clearedArtifact.Path)
	if err != nil {
		t.Fatalf("read cleared XML artifact: %v", err)
	}
	if string(got) != clearedXML {
		t.Fatalf("cleared XML artifact = %q, want %q", string(got), clearedXML)
	}
}

func TestReportingRetainsSubmittedAndProviderResponseArtifacts(t *testing.T) {
	defer os.RemoveAll("processor_test_artifacts")

	const providerResponse = `{"status":"REPORTED","reference":"report-ref-41"}`
	store := &fakeSubmissionStore{}
	provider := fakeSubmissionProvider{
		result: &ProviderResult{
			State:                OutcomeAccepted,
			ProviderCode:         "REPORTED",
			ProviderRef:          "report-ref-41",
			ProviderResponse:     providerResponse,
			ProviderResponseType: "application/json",
		},
	}
	inv := outcomeTestInvoice()
	inv.SubType = invoice.SubTypeSimplified

	if _, err := signSubmitUpdateWithProvider(provider, store, inv, 41, outcomeTestMeta()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.outcome.ClearedXMLPath != "" {
		t.Fatalf("cleared XML path = %q, want empty for reporting", store.outcome.ClearedXMLPath)
	}
	if store.outcome.ProviderRef != "report-ref-41" {
		t.Fatalf("provider reference = %q, want report-ref-41", store.outcome.ProviderRef)
	}

	var responseArtifact artifactRecord
	for _, artifact := range store.arts {
		if artifact.Kind == "response_xml" {
			responseArtifact = artifact
			break
		}
	}
	if responseArtifact.Path == "" {
		t.Fatal("provider response artifact was not recorded")
	}
	got, err := os.ReadFile(responseArtifact.Path)
	if err != nil {
		t.Fatalf("read provider response artifact: %v", err)
	}
	if string(got) != providerResponse {
		t.Fatalf("provider response artifact = %q, want %q", string(got), providerResponse)
	}
}

func TestArtifactStoreFailureIsExplicit(t *testing.T) {
	store := &fakeSubmissionStore{}
	meta := outcomeTestMeta()
	meta.xmlDir = ""
	provider := fakeSubmissionProvider{
		result: &ProviderResult{
			State:        OutcomeAccepted,
			ProviderCode: "CLEARED",
			ClearedXML:   "<cleared-invoice/>",
		},
	}

	_, err := signSubmitUpdateWithProvider(provider, store, outcomeTestInvoice(), 41, meta)
	if err == nil {
		t.Fatal("expected an artifact-store error")
	}
	if store.outcome.State != OutcomeTerminalFailure {
		t.Fatalf("outcome state = %q, want terminal_failure", store.outcome.State)
	}
	if store.update.MarkIssued {
		t.Fatal("artifact-store failure must not mark the document issued")
	}
	if !strings.Contains(err.Error(), "artifact store") {
		t.Fatalf("error = %q, want artifact-store context", err)
	}
}

func TestProviderErrorUnwrapsCause(t *testing.T) {
	cause := errors.New("network failure")
	err := &ProviderError{State: OutcomeAmbiguous, Code: "NETWORK", Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("ProviderError should unwrap its cause")
	}
}

func TestStableInvoiceUUIDIsTenantAndDocumentScoped(t *testing.T) {
	cfg := &config.Config{TenantID: "tenant-a", MerchantID: 7}
	first := stableInvoiceUUID(cfg, "bill", 41)
	second := stableInvoiceUUID(cfg, "bill", 41)
	otherTenant := stableInvoiceUUID(&config.Config{TenantID: "tenant-b", MerchantID: 8}, "bill", 41)
	otherDocument := stableInvoiceUUID(cfg, "bill", 42)

	if first != second {
		t.Fatalf("stable UUID changed across retries: %q != %q", first, second)
	}
	if first == otherTenant || first == otherDocument {
		t.Fatalf("stable UUID is not scoped: %q", first)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("stable UUID is not a valid UUID: %v", err)
	}
}

func outcomeTestInvoice() *invoice.Invoice {
	return &invoice.Invoice{
		ID:               "INV-41",
		UUID:             "00000000-0000-0000-0000-000000000041",
		IssueDate:        time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC),
		IssueTime:        time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC),
		TypeCode:         invoice.TypeInvoice,
		SubType:          invoice.SubTypeStandard,
		DocumentCurrency: "SAR",
		TaxCurrency:      "SAR",
		Supplier: invoice.Party{
			RegistrationName: "Test Seller",
			VAT:              "300000000000003",
			SchemeID:         "CRN",
			ID:               "1010010000",
			Country:          "SA",
		},
		Customer: invoice.Party{
			RegistrationName: "Test Buyer",
			VAT:              "399999999800003",
			Country:          "SA",
		},
		PreviousInvoiceHash: DefaultPIH,
		InvoiceCounterValue: 1,
		PaymentMeans:        invoice.PaymentCash,
		Lines: []invoice.LineItem{{
			ID:          "1",
			Name:        "Test item",
			Quantity:    1,
			UnitCode:    "PCE",
			UnitPrice:   100,
			TaxCategory: invoice.TaxStandard,
			TaxPercent:  15,
		}},
	}
}

func outcomeTestMeta() xmlMeta {
	return xmlMeta{
		xmlDir:       "processor_test_artifacts",
		tenantID:     "tenant-a",
		merchantID:   7,
		sourceBillID: 41,
		companyID:    3,
		branchID:     2,
		billType:     "invoice",
		table:        "bill",
		qrColumn:     "qr_code",
		documentKind: "bill",
	}
}
