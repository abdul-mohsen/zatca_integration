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
		"a credit note": func(f map[string]any) {
			f["doc_type"] = "credit"
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

// pos_id is limited to 64 characters, as the backend counts it. A 40-character Arabic name is 80 bytes and still fits.
func TestPosIDLimitCountsCharactersNotBytes(t *testing.T) {
	fits := strings.Repeat("م", 40)
	tooLong := strings.Repeat("م", 65)
	if _, err := DecodeEnvelope(envelopeWith(t, func(f map[string]any) { f["pos_id"] = fits })); err != nil {
		t.Fatalf("a 40-character pos_id (80 bytes) was refused: %v", err)
	}
	if _, err := DecodeEnvelope(envelopeWith(t, func(f map[string]any) { f["pos_id"] = tooLong })); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("DecodeEnvelope with a 65-character pos_id = %v; want ErrInvalidEnvelope", err)
	}
}

func TestEnvelopeIdentityFieldsRejectUnsafeOrMalformedValues(t *testing.T) {
	cases := map[string]func(map[string]any){
		"database wildcard": func(f map[string]any) { f["db_name"] = "tenant.>" },
		"control character in request": func(f map[string]any) {
			f["bill_request_id"] = "request\nid"
		},
		"oversized request": func(f map[string]any) {
			f["bill_request_id"] = strings.Repeat("r", 37)
		},
		"short fingerprint": func(f map[string]any) { f["request_fingerprint"] = "abc" },
		"non-hex fingerprint": func(f map[string]any) {
			f["request_fingerprint"] = strings.Repeat("g", 64)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeEnvelope(envelopeWith(t, change)); !errors.Is(err, ErrInvalidEnvelope) {
				t.Fatalf("DecodeEnvelope = %v; want ErrInvalidEnvelope", err)
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
