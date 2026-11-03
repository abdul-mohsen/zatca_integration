package queue

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The backend publishes the same bill envelope (pkg/handlers/testdata/bill_envelope.json). Decoding it here is the check
// that the two contracts agree.
func TestDecodeEnvelopeAcceptsTheBackendEnvelope(t *testing.T) {
	msg, err := DecodeEnvelope(readFixture(t, "bill_envelope.json"))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if msg.TenantKey != "acme-0001" || msg.EGSSequenceID != "7" || msg.BranchID != 7 {
		t.Fatalf("decoded %+v; want the tenant key and the branch as the envelope names them", msg)
	}
	if msg.PartitionKey() != "acme-0001\x007" {
		t.Fatalf("partition = %q; want tenant key and EGS", msg.PartitionKey())
	}
}

// envelopeWith returns the backend envelope with one field changed, as a JSON object.
func envelopeWith(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(readFixture(t, "bill_envelope.json"), &fields); err != nil {
		t.Fatal(err)
	}
	change(fields)
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodeEnvelopeRefusesAnyOtherContract(t *testing.T) {
	cases := map[string]func(map[string]any){
		"tenant_id instead of tenant_key": func(f map[string]any) {
			delete(f, "tenant_key")
			f["tenant_id"] = 5
		},
		"an OTP on a bill": func(f map[string]any) {
			f["otp"] = "123456"
		},
		"a missing tenant key": func(f map[string]any) {
			delete(f, "tenant_key")
		},
		"an EGS id that is not the branch": func(f map[string]any) {
			f["egs_sequence_id"] = "8"
		},
		"an invoice UUID that is not a UUID": func(f map[string]any) {
			f["invoice_uuid"] = "not-a-uuid"
		},
		"a missing request fingerprint": func(f map[string]any) {
			delete(f, "request_fingerprint")
		},
		"an unknown document type": func(f map[string]any) {
			f["doc_type"] = "refund"
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeEnvelope(envelopeWith(t, change))
			if !errors.Is(err, ErrInvalidEnvelope) {
				t.Fatalf("DecodeEnvelope = %v; want ErrInvalidEnvelope", err)
			}
		})
	}
}

// The backend publishes a credit and a debit envelope with the same fields as a bill. Both decode, and each keeps its own doc_type
// and id.
func TestDecodeEnvelopeAcceptsTheCreditAndDebitEnvelopes(t *testing.T) {
	cases := map[string]struct {
		fixture string
		docType DocType
		id      int64
	}{
		"credit": {"credit_envelope.json", DocCredit, 9},
		"debit":  {"debit_envelope.json", DocDebit, 10},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg, err := DecodeEnvelope(readFixture(t, tc.fixture))
			if err != nil {
				t.Fatalf("DecodeEnvelope: %v", err)
			}
			if msg.DocType != tc.docType || msg.ID != tc.id || msg.BranchID != 7 {
				t.Fatalf("decoded %s %d on branch %d; want %s %d on branch 7", msg.DocType, msg.ID, msg.BranchID, tc.docType, tc.id)
			}
		})
	}
}

func TestOrderedSubjectsNameTheBranchOnly(t *testing.T) {
	if got := OrderedSubject("tenant_acme", 7); !strings.HasSuffix(got, ".tenant_acme.7") {
		t.Fatalf("OrderedSubject = %q", got)
	}
	if OnboardSubject("tenant_acme", 7) == OrderedSubject("tenant_acme", 7) {
		t.Fatal("onboarding shares a subject with the stream")
	}
	if strings.HasPrefix(OnboardSubject("tenant_acme", 7), "zatca.") {
		t.Fatal("onboarding subject falls inside the stream's zatca.> subject")
	}
}
