package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestVerificationFlagsDispatchBeforeStartup(t *testing.T) {
	runCalled := false
	handled, exitCode, err := dispatchVerificationFlags(true, true, func() int {
		runCalled = true
		return 1
	})
	if err != nil {
		t.Fatalf("dispatchVerificationFlags() error = %v", err)
	}
	if !handled {
		t.Fatal("verification flags were not handled")
	}
	if !runCalled {
		t.Fatal("verification runner was not called")
	}
	if exitCode != 1 {
		t.Fatalf("exit code = %d; want runner status 1", exitCode)
	}
}

func TestNoVerificationFlagsLeaveNormalStartupPath(t *testing.T) {
	runCalled := false
	handled, exitCode, err := dispatchVerificationFlags(false, false, func() int {
		runCalled = true
		return 0
	})
	if err != nil {
		t.Fatalf("dispatchVerificationFlags() error = %v", err)
	}
	if handled || exitCode != 0 {
		t.Fatalf("dispatch result = (%t, %d); want (false, 0)", handled, exitCode)
	}
	if runCalled {
		t.Fatal("verification runner ran without verification flags")
	}
}

func TestVerificationFlagsMustBeUsedTogether(t *testing.T) {
	for _, flags := range []struct {
		name     string
		verifyDB bool
		json     bool
	}{
		{name: "missing json", verifyDB: true},
		{name: "json without verify-db", json: true},
	} {
		t.Run(flags.name, func(t *testing.T) {
			runCalled := false
			handled, exitCode, err := dispatchVerificationFlags(flags.verifyDB, flags.json, func() int {
				runCalled = true
				return 0
			})
			if !handled || exitCode != 2 || err == nil {
				t.Fatalf("dispatch result = (%t, %d, %v); want handled invocation error", handled, exitCode, err)
			}
			if runCalled {
				t.Fatal("verification runner ran with an incomplete flag pair")
			}
		})
	}
}

func TestVerificationModeEmitsSafeJSONForInvalidConfiguration(t *testing.T) {
	var output bytes.Buffer
	exitCode := runDatabaseVerification(&output, verificationDBConfig{
		MasterDSN:      "invalid-DB_PASSWORD_FIXTURE-dsn",
		MasterDatabase: "zatca_master",
		EncryptionKey:  "ENCRYPT_KEY_FIXTURE",
	})
	if exitCode != 2 {
		t.Fatalf("exit code = %d; want configuration exit 2", exitCode)
	}

	var report verificationReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("verification output is not JSON: %v; output=%q", err, output.String())
	}
	if report.OverallStatus != verificationFail || len(report.Checks) == 0 {
		t.Fatalf("report = %+v; want a non-empty failed report", report)
	}
	if !strings.HasSuffix(output.String(), "\n") || strings.Contains(output.String(), "\n\n") {
		t.Fatalf("verification output must be exactly one JSON line: %q", output.String())
	}
	for _, forbidden := range []string{"DB_PASSWORD_FIXTURE", "ENCRYPT_KEY_FIXTURE"} {
		if strings.Contains(output.String(), forbidden) {
			t.Errorf("verification report exposed %q: %s", forbidden, output.String())
		}
	}
}

func TestVerificationModeRejectsInvalidOrConflictingTenantPrefix(t *testing.T) {
	cases := []struct {
		name             string
		tenantDBPrefix   string
		tenantNamePrefix string
		secretToken      string
	}{
		{
			name:             "conflicting prefixes",
			tenantDBPrefix:   "tenant_prod_",
			tenantNamePrefix: "dev-",
			secretToken:      "tenant_prod_",
		},
		{
			name:           "invalid db prefix",
			tenantDBPrefix: "tenant-bad;PREFIX_SECRET_FIXTURE",
			secretToken:    "PREFIX_SECRET_FIXTURE",
		},
		{
			name:             "invalid name prefix",
			tenantNamePrefix: "---",
			secretToken:      "---",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			exitCode := runDatabaseVerification(&output, verificationDBConfig{
				MasterDSN:        "user:pass@tcp(127.0.0.1:3306)/zatca_master",
				MasterDatabase:   "zatca_master",
				RuntimeBaseDSN:   "user:pass@tcp(127.0.0.1:3306)/",
				EncryptionKey:    "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
				TenantDBPrefix:   tc.tenantDBPrefix,
				TenantNamePrefix: tc.tenantNamePrefix,
			})
			if exitCode != 2 {
				t.Fatalf("exit code = %d; want configuration exit 2", exitCode)
			}
			var report verificationReport
			if err := json.Unmarshal(output.Bytes(), &report); err != nil {
				t.Fatalf("json.Unmarshal() error = %v; output=%q", err, output.String())
			}
			if report.OverallStatus != verificationFail {
				t.Fatalf("overall_status = %q; want FAIL", report.OverallStatus)
			}
			prefixCheck := requireVerificationCheck(t, report.Checks, "configuration.tenant_db_prefix")
			if prefixCheck.Status != verificationFail || prefixCheck.ErrorCategory != verificationErrorConfiguration {
				t.Fatalf("prefix check = %+v; want configuration failure", prefixCheck)
			}
			registryCheck := requireVerificationCheck(t, report.Checks, "master.registry")
			if registryCheck.Status != verificationNotVerified || registryCheck.ErrorCategory != verificationErrorConfiguration {
				t.Fatalf("registry check = %+v; want NOT_VERIFIED configuration", registryCheck)
			}
			if tc.secretToken != "" && strings.Contains(output.String(), tc.secretToken) {
				t.Fatalf("verification output leaked prefix token %q: %s", tc.secretToken, output.String())
			}
		})
	}
}
