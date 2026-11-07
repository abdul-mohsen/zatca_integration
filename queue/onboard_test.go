package queue

import (
	"errors"
	"testing"
)

func TestDecodeOnboardRequestAcceptsTheBackendRequest(t *testing.T) {
	req, err := DecodeOnboardRequest(readFixture(t, "onboard_request.json"))
	if err != nil {
		t.Fatalf("DecodeOnboardRequest: %v", err)
	}
	if req.BranchID != 7 || req.TenantKey != "acme-0001" || req.OTP != "123456" {
		t.Fatalf("decoded %+v", req)
	}
}

func TestDecodeOnboardRequestRefusesAMissingOTPAndAForeignField(t *testing.T) {
	cases := map[string]string{
		"missing OTP":   `{"db_name":"tenant_acme","tenant_key":"acme-0001","branch_id":7}`,
		"foreign field": `{"db_name":"tenant_acme","tenant_key":"acme-0001","branch_id":7,"otp":"1","tenant_id":5}`,
		"no branch":     `{"db_name":"tenant_acme","tenant_key":"acme-0001","otp":"1"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeOnboardRequest([]byte(body)); !errors.Is(err, ErrInvalidEnvelope) {
				t.Fatalf("DecodeOnboardRequest = %v; want ErrInvalidEnvelope", err)
			}
		})
	}
}
