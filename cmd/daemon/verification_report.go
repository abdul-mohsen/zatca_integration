package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type verificationStatus string

const (
	verificationPass        verificationStatus = "PASS"
	verificationFail        verificationStatus = "FAIL"
	verificationNotVerified verificationStatus = "NOT_VERIFIED"
)

type verificationErrorCategory string

const (
	verificationErrorConfiguration verificationErrorCategory = "configuration"
	verificationErrorConnection    verificationErrorCategory = "connection"
	verificationErrorTimeout       verificationErrorCategory = "timeout"
	verificationErrorRegistry      verificationErrorCategory = "registry"
	verificationErrorSchema        verificationErrorCategory = "schema"
	verificationErrorPermission    verificationErrorCategory = "permission"
	verificationErrorDecryption    verificationErrorCategory = "decryption"
	verificationErrorNotReady      verificationErrorCategory = "not_ready"
	verificationErrorInternal      verificationErrorCategory = "internal"
)

type verificationTarget struct {
	AccountRole string `json:"account_role,omitempty"`
	TenantID    string `json:"tenant_id,omitempty"`
	Database    string `json:"database,omitempty"`
	BranchID    *int64 `json:"branch_id,omitempty"`
	Operation   string `json:"operation,omitempty"`
}

type verificationCheck struct {
	ID            string                    `json:"id"`
	Status        verificationStatus        `json:"status"`
	Target        verificationTarget        `json:"target"`
	ErrorCategory verificationErrorCategory `json:"error_category,omitempty"`
	MySQLCode     *int                      `json:"mysql_code,omitempty"`
}

type verificationReport struct {
	SchemaVersion int                 `json:"schema_version"`
	OverallStatus verificationStatus  `json:"overall_status"`
	Checks        []verificationCheck `json:"checks"`
}

func buildVerificationReport(checks []verificationCheck) (verificationReport, error) {
	if len(checks) == 0 {
		return verificationReport{}, errors.New("verification report requires checks")
	}

	seen := make(map[string]struct{}, len(checks))
	report := verificationReport{
		SchemaVersion: 1,
		Checks:        append([]verificationCheck(nil), checks...),
	}
	report.OverallStatus = verificationPass

	for _, check := range report.Checks {
		if !validVerificationCheckID(check.ID) {
			return verificationReport{}, errors.New("verification check ID is invalid")
		}
		if _, exists := seen[check.ID]; exists {
			return verificationReport{}, errors.New("verification check IDs must be unique")
		}
		seen[check.ID] = struct{}{}

		switch check.Status {
		case verificationPass:
			if check.ErrorCategory != "" || check.MySQLCode != nil {
				return verificationReport{}, errors.New("successful verification check cannot have error metadata")
			}
		case verificationFail:
			report.OverallStatus = verificationFail
		case verificationNotVerified:
			if report.OverallStatus != verificationFail {
				report.OverallStatus = verificationNotVerified
			}
		default:
			return verificationReport{}, errors.New("verification check status is invalid")
		}

		if check.ErrorCategory != "" && !validVerificationErrorCategory(check.ErrorCategory) {
			return verificationReport{}, errors.New("verification error category is invalid")
		}
		if check.MySQLCode != nil && *check.MySQLCode < 0 {
			return verificationReport{}, errors.New("MySQL error code must not be negative")
		}
		if check.Target.AccountRole != "" &&
			check.Target.AccountRole != "runtime" &&
			check.Target.AccountRole != "onboarding" {
			return verificationReport{}, errors.New("verification account role is invalid")
		}
	}

	return report, nil
}

func writeVerificationReport(w io.Writer, checks []verificationCheck) error {
	if w == nil {
		return errors.New("verification report writer is not configured")
	}
	report, err := buildVerificationReport(checks)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(true)
	return encoder.Encode(report)
}

func validVerificationCheckID(id string) bool {
	if id == "" || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func validVerificationErrorCategory(category verificationErrorCategory) bool {
	switch category {
	case verificationErrorConfiguration,
		verificationErrorConnection,
		verificationErrorTimeout,
		verificationErrorRegistry,
		verificationErrorSchema,
		verificationErrorPermission,
		verificationErrorDecryption,
		verificationErrorNotReady,
		verificationErrorInternal:
		return true
	default:
		return false
	}
}
