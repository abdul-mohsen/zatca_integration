package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestBuildVerificationReportEmitsExactVersionedSchema(t *testing.T) {
	branchID := int64(17)
	report, err := buildVerificationReport([]verificationCheck{
		{
			ID:     "tenant.branch.decrypt",
			Status: verificationPass,
			Target: verificationTarget{
				AccountRole: "runtime",
				TenantID:    "tenant-a",
				Database:    "tenant_a",
				BranchID:    &branchID,
				Operation:   "decrypt_branch_config",
			},
		},
	})
	if err != nil {
		t.Fatalf("buildVerificationReport() error = %v", err)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &top); err != nil {
		t.Fatalf("decode top-level report: %v", err)
	}
	assertJSONKeys(t, top, "schema_version", "overall_status", "checks")

	var version int
	if err := json.Unmarshal(top["schema_version"], &version); err != nil {
		t.Fatalf("decode schema_version: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema_version = %d; want 1", version)
	}

	var checks []map[string]json.RawMessage
	if err := json.Unmarshal(top["checks"], &checks); err != nil {
		t.Fatalf("decode checks: %v", err)
	}
	if len(checks) != 1 {
		t.Fatalf("len(checks) = %d; want 1", len(checks))
	}
	assertJSONKeys(t, checks[0], "id", "status", "target")

	var target map[string]json.RawMessage
	if err := json.Unmarshal(checks[0]["target"], &target); err != nil {
		t.Fatalf("decode target: %v", err)
	}
	assertJSONKeys(t, target, "account_role", "tenant_id", "database", "branch_id", "operation")

	var gotBranchID int64
	if err := json.Unmarshal(target["branch_id"], &gotBranchID); err != nil {
		t.Fatalf("decode branch_id: %v", err)
	}
	if gotBranchID != branchID {
		t.Fatalf("branch_id = %d; want %d", gotBranchID, branchID)
	}
}

func TestBuildVerificationReportAggregatesStatusesFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		status []verificationStatus
		want   verificationStatus
	}{
		{
			name:   "all pass",
			status: []verificationStatus{verificationPass, verificationPass},
			want:   verificationPass,
		},
		{
			name:   "unknown prevents pass",
			status: []verificationStatus{verificationPass, verificationNotVerified},
			want:   verificationNotVerified,
		},
		{
			name:   "failure takes precedence over unknown",
			status: []verificationStatus{verificationNotVerified, verificationFail},
			want:   verificationFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := make([]verificationCheck, 0, len(tt.status))
			for i, status := range tt.status {
				checks = append(checks, verificationCheck{
					ID:     "check-" + string(rune('a'+i)),
					Status: status,
					Target: verificationTarget{Operation: "probe"},
				})
			}
			report, err := buildVerificationReport(checks)
			if err != nil {
				t.Fatalf("buildVerificationReport() error = %v", err)
			}
			if report.OverallStatus != tt.want {
				t.Fatalf("overall_status = %q; want %q", report.OverallStatus, tt.want)
			}
		})
	}
}

func TestBuildVerificationReportRejectsInvalidOrEmptyEvidence(t *testing.T) {
	mysqlCode := -1
	tests := []struct {
		name   string
		checks []verificationCheck
	}{
		{name: "empty checks"},
		{
			name: "empty ID",
			checks: []verificationCheck{{
				Status: verificationPass,
				Target: verificationTarget{Operation: "probe"},
			}},
		},
		{
			name: "duplicate IDs",
			checks: []verificationCheck{
				{ID: "duplicate", Status: verificationPass},
				{ID: "duplicate", Status: verificationPass},
			},
		},
		{
			name: "unknown status",
			checks: []verificationCheck{{
				ID: "unknown-status", Status: verificationStatus("MAYBE"),
			}},
		},
		{
			name: "unknown error category",
			checks: []verificationCheck{{
				ID: "unknown-category", Status: verificationFail, ErrorCategory: "raw-error",
			}},
		},
		{
			name: "negative mysql code",
			checks: []verificationCheck{{
				ID: "negative-code", Status: verificationFail, MySQLCode: &mysqlCode,
			}},
		},
		{
			name: "success with error metadata",
			checks: []verificationCheck{{
				ID: "success-error", Status: verificationPass, ErrorCategory: verificationErrorConnection,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := buildVerificationReport(tt.checks); err == nil {
				t.Fatal("buildVerificationReport() succeeded; want validation error")
			}
		})
	}
}

func TestBuildVerificationReportAllowsOnlySafeErrorMetadata(t *testing.T) {
	mysqlCode := 1045
	report, err := buildVerificationReport([]verificationCheck{
		{
			ID:            "tenant.connect",
			Status:        verificationFail,
			Target:        verificationTarget{AccountRole: "runtime", TenantID: "tenant-a", Database: "tenant_a", Operation: "connect"},
			ErrorCategory: verificationErrorConnection,
			MySQLCode:     &mysqlCode,
		},
	})
	if err != nil {
		t.Fatalf("buildVerificationReport() error = %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &top); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	var checks []map[string]json.RawMessage
	if err := json.Unmarshal(top["checks"], &checks); err != nil {
		t.Fatalf("decode checks: %v", err)
	}
	assertJSONKeys(t, checks[0], "id", "status", "target", "error_category", "mysql_code")
}

func TestWriteVerificationReportWritesOneJSONLine(t *testing.T) {
	var output bytes.Buffer
	if err := writeVerificationReport(&output, []verificationCheck{{
		ID:     "master.connect",
		Status: verificationPass,
		Target: verificationTarget{AccountRole: "runtime", Database: "zatca_master", Operation: "connect"},
	}}); err != nil {
		t.Fatalf("writeVerificationReport() error = %v", err)
	}

	encoded := output.Bytes()
	if len(encoded) == 0 || encoded[len(encoded)-1] != '\n' {
		t.Fatalf("report output must end with one newline: %q", encoded)
	}
	if bytes.Count(encoded, []byte{'\n'}) != 1 {
		t.Fatalf("report output contains progress or multiple JSON lines: %q", encoded)
	}
	if !json.Valid(bytes.TrimSuffix(encoded, []byte{'\n'})) {
		t.Fatalf("report output is not exactly one JSON object: %q", encoded)
	}
}

func TestVerificationReportDoesNotSerializeSecretsOrRawErrors(t *testing.T) {
	report, err := buildVerificationReport([]verificationCheck{{
		ID:            "tenant.connect",
		Status:        verificationFail,
		Target:        verificationTarget{AccountRole: "runtime", TenantID: "tenant-a", Database: "tenant_a", Operation: "connect"},
		ErrorCategory: verificationErrorConnection,
	}})
	if err != nil {
		t.Fatalf("buildVerificationReport() error = %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, forbidden := range []string{
		"password",
		"private_key",
		"dsn",
		"error_message",
		"secret_length",
		"secret_hash",
	} {
		if bytes.Contains(bytes.ToLower(encoded), []byte(forbidden)) {
			t.Errorf("report contains forbidden field/text %q: %s", forbidden, encoded)
		}
	}
}

func assertJSONKeys(t *testing.T, object map[string]json.RawMessage, want ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON keys = %v; want %v", got, want)
	}
}

func TestVerificationReportSafeTargetCanBeEmpty(t *testing.T) {
	report, err := buildVerificationReport([]verificationCheck{{
		ID:     "master.database",
		Status: verificationNotVerified,
		Target: verificationTarget{},
	}})
	if err != nil {
		t.Fatalf("buildVerificationReport() error = %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"target":{}`) {
		t.Fatalf("empty safe target must be an object: %s", encoded)
	}
}
