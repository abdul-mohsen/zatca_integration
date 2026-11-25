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
