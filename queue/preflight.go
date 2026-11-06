package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrTenantSchemaNotReady means the tenant database has not received the
// backend migrations required by the daemon runtime.
var ErrTenantSchemaNotReady = errors.New("ZATCA tenant schema is not ready")

// SchemaError identifies every required column missing from a tenant schema.
// Reporting all missing columns at startup is more useful than discovering
// them one at a time while processing a live invoice.
type SchemaError struct {
	Missing []string
}

func (e *SchemaError) Error() string {
	return fmt.Sprintf("%s: missing %s", ErrTenantSchemaNotReady, strings.Join(e.Missing, ", "))
}

func (e *SchemaError) Unwrap() error {
	return ErrTenantSchemaNotReady
}

type schemaRequirement struct {
	table   string
	columns []string
}

// runtimeSchema is the daemon's SQL contract. The reservation columns are
// intentionally listed here: SQLChain owns their values, while the backend
// migration owns their DDL.
var runtimeSchema = []schemaRequirement{
	{
		table: "branch_zatca_config",
		columns: []string{
			"branch_id",
			"last_icv",
			"previous_invoice_hash",
			"icv_reservation_key",
			"icv_reservation_token",
			"reserved_icv",
			"reservation_lease_expires_at",
		},
	},
	{
		table: "bill",
		columns: []string{
			"id",
			"branch_id",
			"state",
			"invoice_uuid",
			"invoice_hash",
			"qr_code",
		},
	},
	{
		table: "zatca_submission",
		columns: []string{
			"bill_id",
			"branch_id",
			"status",
			"outcome_state",
			"invoice_icv",
			"previous_invoice_hash",
			"invoice_uuid",
			"invoice_hash",
			"invoice_kind",
			"signed_xml",
			"provider_code",
			"rejection_code",
			"rejection_msg",
			"provider_warning_message",
			"submitted_at",
			"outcome_at",
		},
	},
	{
		table: "zatca_submission_outcome",
		columns: []string{
			"submission_id",
			"outcome_state",
			"provider_code",
			"provider_warning_message",
			"observed_at",
		},
	},
	{
		table: "zatca_consumer_claim",
		columns: []string{
			"pos_id",
			"bill_request_id",
			"event_id",
			"egs_sequence_id",
			"fingerprint",
			"state",
			"lease_expires_at",
			"completed_at",
		},
	},
}

// Preflight verifies the schema used by SQLChain and SQLClaimStore. It does
// not apply migrations and does not claim that a provider or sandbox is
// reachable; those are separate runtime evidence boundaries.
func Preflight(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("%w: database is not configured", ErrTenantSchemaNotReady)
	}

	var (
		clauses []string
		args    []any
	)
	for _, requirement := range runtimeSchema {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(requirement.columns)), ",")
		clauses = append(clauses, "(table_name = ? AND column_name IN ("+placeholders+"))")
		args = append(args, requirement.table)
		for _, column := range requirement.columns {
			args = append(args, column)
		}
	}

	rows, err := db.QueryContext(ctx, `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND (`+strings.Join(clauses, " OR ")+`)
	`, args...)
	if err != nil {
		return fmt.Errorf("%w: inspect tenant schema: %v", ErrTenantSchemaNotReady, err)
	}
	defer rows.Close()

	found := make(map[string]map[string]bool, len(runtimeSchema))
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return fmt.Errorf("%w: scan tenant schema: %v", ErrTenantSchemaNotReady, err)
		}
		if found[table] == nil {
			found[table] = map[string]bool{}
		}
		found[table][column] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: read tenant schema: %v", ErrTenantSchemaNotReady, err)
	}

	var missing []string
	for _, requirement := range runtimeSchema {
		for _, column := range requirement.columns {
			if !found[requirement.table][column] {
				missing = append(missing, requirement.table+"."+column)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return &SchemaError{Missing: missing}
	}
	return nil
}
