package queue

import (
	"fmt"
	"strings"
)

// OnboardRequest asks the daemon to onboard one branch with a one-time OTP. It is a request, not a stream message, so JetStream
// never stores the OTP. The daemon holds the OTP in memory only and answers with an OnboardReply, which never repeats it.
type OnboardRequest struct {
	DBName    string `json:"db_name"`
	TenantKey string `json:"tenant_key"`
	BranchID  int64  `json:"branch_id"`
	OTP       string `json:"otp"`
}

// OnboardReply answers an OnboardRequest.
type OnboardReply struct {
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

// OnboardSubject is the subject that carries one branch's onboarding request. It is outside SubjectAll.
func OnboardSubject(dbName string, branchID int64) string {
	return fmt.Sprintf("control.zatca.onboard.%s.%d", dbName, branchID)
}

// OnboardWildcard matches the onboarding subjects of one tenant.
func OnboardWildcard(dbName string) string {
	return fmt.Sprintf("control.zatca.onboard.%s.*", dbName)
}

// DecodeOnboardRequest decodes and validates an onboarding request.
func DecodeOnboardRequest(data []byte) (OnboardRequest, error) {
	var req OnboardRequest
	if err := strictDecode(data, &req); err != nil {
		return OnboardRequest{}, err
	}
	switch {
	case strings.TrimSpace(req.DBName) == "":
		return OnboardRequest{}, invalidf("db_name is required")
	case strings.TrimSpace(req.TenantKey) == "":
		return OnboardRequest{}, invalidf("tenant_key is required")
	case req.BranchID <= 0:
		return OnboardRequest{}, invalidf("branch_id is required")
	case strings.TrimSpace(req.OTP) == "" || len(req.OTP) > 64:
		return OnboardRequest{}, invalidf("otp is required and must be at most 64 characters")
	}
	return req, nil
}
