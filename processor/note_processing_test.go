package processor

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/models"
)

func TestNoteInvoiceUsesCanonicalSourceIdentityAndOriginalReference(t *testing.T) {
	cfg := &config.Config{
		Seller: config.SellerInfo{
			Name:    "Seller",
			VAT:     "310000000000003",
			CRN:     "1010010000",
			Country: "SA",
		},
	}
	row := CreditDebitRow{
		BillRow: BillRow{
			ID:            17,
			SeqNumber:     nullString("INV-17"),
			BuyerVAT:      nullString("399999999800003"),
			BuyerName:     nullString("Buyer"),
			EffectiveDate: nullString("2026-10-31"),
		},
		NoteText: nullString("Returned goods"),
		NoteID:   nullInt64(91),
		Source: DocumentSourceRow{
			DocumentType:         "credit",
			SourceID:             91,
			InvoiceUUID:          "note-uuid",
			OriginalDocumentType: "bill",
			OriginalSourceID:     17,
			OriginalInvoiceUUID:  "bill-uuid",
			SequenceNumber:       "INV-17",
		},
	}
	state := DocumentChainState{
		DocumentUUID: "note-uuid",
		ICV:          9,
	}

	credit := BuildCreditNoteInvoiceWithChain(cfg, row, []ProductRow{testProduct()}, time.Now(), state)
	debit := BuildDebitNoteInvoiceWithChain(cfg, row, []ProductRow{testProduct()}, time.Now(), state)

	for _, tc := range []struct {
		name string
		inv  *invoice.Invoice
		code invoice.TypeCode
		id   string
	}{
		{name: "credit", inv: credit, code: invoice.TypeCreditNote, id: "INV-CN-91"},
		{name: "debit", inv: debit, code: invoice.TypeDebitNote, id: "INV-DN-91"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.inv.ID != tc.id {
				t.Fatalf("note ID = %q, want source-based ID", tc.inv.ID)
			}
			if tc.inv.TypeCode != tc.code {
				t.Fatalf("type code = %d, want %d", tc.inv.TypeCode, tc.code)
			}
			if tc.inv.BillingReferenceID != "INV-17" {
				t.Fatalf("billing reference ID = %q, want original invoice number", tc.inv.BillingReferenceID)
			}
			if tc.inv.BillingReferenceUUID != "bill-uuid" {
				t.Fatalf("billing reference UUID = %q, want original invoice UUID", tc.inv.BillingReferenceUUID)
			}
			if tc.inv.Note != "Returned goods" || tc.inv.InstructionNote != "Returned goods" {
				t.Fatalf("note reason not mapped to both XML note fields: note=%q instruction=%q",
					tc.inv.Note, tc.inv.InstructionNote)
			}
			xml, err := tc.inv.ToXML()
			if err != nil {
				t.Fatalf("ToXML() error: %v", err)
			}
			for _, want := range []string{
				"<cbc:Note languageID=\"ar\">Returned goods</cbc:Note>",
				"<cbc:ID>INV-17</cbc:ID>",
				"<cbc:UUID>bill-uuid</cbc:UUID>",
			} {
				if !strings.Contains(xml, want) {
					t.Fatalf("note XML missing %q:\n%s", want, xml)
				}
			}
		})
	}
}

func TestCreditDebitSubmissionRequiresOriginalDocumentReference(t *testing.T) {
	for _, documentType := range []string{"credit", "debit"} {
		t.Run(documentType, func(t *testing.T) {
			identity := SubmissionIdentity{
				TenantID:      42,
				EGSSequenceID: "egs-1",
				BillRequestID: documentType + "-request",
				EventID:       documentType + "-event",
				InvoiceUUID:   documentType + "-uuid",
				PosID:         "pos-1",
				DocumentType:  documentType,
				SourceID:      91,
			}
			if err := identity.Validate(); err == nil {
				t.Fatal("note identity without original-document reference was accepted")
			}
		})
	}
}

func TestCreditDebitProviderOutcomesAreSourceScopedAndDuplicateSafe(t *testing.T) {
	tests := []struct {
		name          string
		documentType  string
		sourceID      uint64
		status        SubmissionState
		response      *ProviderResponse
		wantErr       bool
		wantDuplicate bool
	}{
		{
			name:         "accepted credit",
			documentType: "credit",
			sourceID:     91,
			status:       SubmissionAccepted,
			response:     &ProviderResponse{Status: "CLEARED", InvoiceHash: "credit-hash"},
		},
		{
			name:         "rejected credit",
			documentType: "credit",
			sourceID:     92,
			status:       SubmissionRejected,
			response:     &ProviderResponse{Status: "REJECTED", Errors: []models.ErrorDetail{{Code: "E", Message: "invalid"}}},
			wantErr:      true,
		},
		{
			name:         "ambiguous credit",
			documentType: "credit",
			sourceID:     93,
			status:       SubmissionAmbiguous,
			wantErr:      true,
		},
		{
			name:         "accepted debit",
			documentType: "debit",
			sourceID:     94,
			status:       SubmissionAccepted,
			response:     &ProviderResponse{Status: "REPORTED", InvoiceHash: "debit-hash"},
		},
		{
			name:         "rejected debit",
			documentType: "debit",
			sourceID:     95,
			status:       SubmissionRejected,
			response:     &ProviderResponse{Status: "REJECTED", Errors: []models.ErrorDetail{{Code: "E", Message: "invalid"}}},
			wantErr:      true,
		},
		{
			name:         "ambiguous debit",
			documentType: "debit",
			sourceID:     96,
			status:       SubmissionAmbiguous,
			wantErr:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeSubmissionProvider{
				submit: func(SubmissionRequest) (*ProviderResponse, error) {
					if tc.response != nil {
						return tc.response, nil
					}
					return nil, nil
				},
			}
			store := NewMemorySubmissionStore()
			engine := newTestSubmissionEngine(provider, store)
			request := SubmissionRequest{
				Identity: SubmissionIdentity{
					TenantID:             42,
					EGSSequenceID:        "egs-1",
					BillRequestID:        tc.name,
					EventID:              tc.name + "-event",
					InvoiceUUID:          tc.name + "-uuid",
					PosID:                "pos-1",
					DocumentType:         tc.documentType,
					SourceID:             tc.sourceID,
					OriginalDocumentType: "bill",
					OriginalSourceID:     17,
					OriginalInvoiceUUID:  "bill-uuid",
				},
				SignedRequest:   []byte("<signed/>"),
				ProviderRequest: []byte(`{"uuid":"note-uuid","invoiceHash":"hash"}`),
				InvoiceHash:     "hash",
			}

			result, err := engine.Submit(context.Background(), request)
			if tc.wantErr != (err != nil) {
				t.Fatalf("error = %v, wantErr=%v", err, tc.wantErr)
			}
			if result.Attempt.State != tc.status {
				t.Fatalf("state = %q, want %q", result.Attempt.State, tc.status)
			}
			if result.Attempt.Identity.DocumentType != tc.documentType ||
				result.Attempt.Identity.SourceID != tc.sourceID ||
				result.Attempt.Identity.OriginalSourceID != 17 {
				t.Fatalf("attempt identity lost canonical source: %+v", result.Attempt.Identity)
			}

			if tc.status == SubmissionAccepted {
				duplicate, duplicateErr := engine.Submit(context.Background(), request)
				if duplicateErr != nil {
					t.Fatalf("duplicate error: %v", duplicateErr)
				}
				if !duplicate.Duplicate {
					t.Fatal("accepted duplicate delivery was not marked duplicate")
				}
				if len(provider.submits) != 1 {
					t.Fatalf("provider submit calls = %d, want 1", len(provider.submits))
				}
			}
		})
	}
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func nullInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func testProduct() ProductRow {
	return ProductRow{
		Name:     nullString("item"),
		Price:    sql.NullFloat64{Float64: 10, Valid: true},
		Quantity: sql.NullFloat64{Float64: 1, Valid: true},
		VAT:      sql.NullFloat64{Float64: 15, Valid: true},
	}
}
