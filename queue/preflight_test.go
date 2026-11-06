package queue

import (
	"errors"
	"strings"
	"testing"
)

func TestRuntimeSchemaIncludesChainOwnershipAndOutcomeColumns(t *testing.T) {
	required := map[string]map[string]bool{}
	for _, requirement := range runtimeSchema {
		if required[requirement.table] == nil {
			required[requirement.table] = map[string]bool{}
		}
		for _, column := range requirement.columns {
			required[requirement.table][column] = true
		}
	}

	for _, column := range []string{
		"icv_reservation_key",
		"icv_reservation_token",
		"reserved_icv",
		"reservation_lease_expires_at",
	} {
		if !required["branch_zatca_config"][column] {
			t.Fatalf("runtime schema does not preflight branch_zatca_config.%s", column)
		}
	}
	for _, column := range []string{"outcome_state", "invoice_icv", "previous_invoice_hash", "invoice_uuid", "signed_xml"} {
		if !required["zatca_submission"][column] {
			t.Fatalf("runtime schema does not preflight zatca_submission.%s", column)
		}
	}
}

func TestSchemaErrorIdentifiesMissingColumns(t *testing.T) {
	err := &SchemaError{Missing: []string{"branch_zatca_config.icv_reservation_key", "zatca_submission.invoice_icv"}}
	if !errors.Is(err, ErrTenantSchemaNotReady) {
		t.Fatal("SchemaError does not unwrap ErrTenantSchemaNotReady")
	}
	for _, text := range err.Missing {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("error %q does not identify %s", err, text)
		}
	}
}
