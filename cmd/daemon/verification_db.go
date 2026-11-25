package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/zatca-go/zatca/processor"
	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/secutil"
	"github.com/zatca-go/zatca/storeconfig"
	"github.com/zatca-go/zatca/tenant"
)

const verificationDBOperationTimeout = 10 * time.Second

type verificationSQLProbe struct {
	ID        string
	Statement string
}

type verificationDBOps struct {
	openMaster       func(context.Context, string) (*sql.DB, error)
	openTenant       func(context.Context, tenant.Tenant, string) (*sql.DB, error)
	loadTenants      func(context.Context, *sql.DB) ([]tenant.Tenant, error)
	preflight        func(context.Context, *sql.DB) error
	activeBranches   func(context.Context, *sql.DB) ([]int64, error)
	loadBranchConfig func(context.Context, *sql.DB, int64, *secutil.Key) error
	explain          func(context.Context, *sql.DB, string, []any) error
	closeDB          func(*sql.DB) error
	runtimeProbes    []verificationSQLProbe
	onboardingProbes []verificationSQLProbe
}

func defaultVerificationDBOps() verificationDBOps {
	return verificationDBOps{
		openMaster: openVerificationMasterDB,
		openTenant: tenant.OpenDBContext,
		loadTenants: func(ctx context.Context, db *sql.DB) ([]tenant.Tenant, error) {
			return tenant.LoadAllContext(ctx, db)
		},
		preflight: queue.Preflight,
		activeBranches: func(ctx context.Context, db *sql.DB) ([]int64, error) {
			return processor.QueryActiveBranchesContext(ctx, db)
		},
		loadBranchConfig: func(ctx context.Context, db *sql.DB, branchID int64, key *secutil.Key) error {
			_, err := storeconfig.LoadBranchZATCAContext(ctx, db, branchID, key)
			return err
		},
		explain: executeVerificationExplain,
		closeDB: func(db *sql.DB) error {
			return db.Close()
		},
		runtimeProbes:    verificationRuntimeSQLProbes(),
		onboardingProbes: verificationOnboardingSQLProbes(),
	}
}

func openVerificationMasterDB(ctx context.Context, dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("master database DSN is not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetConnMaxIdleTime(time.Minute)
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func verifyDatabase(
	ctx context.Context,
	masterDSN string,
	masterDatabase string,
	runtimeBaseDSN string,
	onboardingBaseDSN string,
	encKey *secutil.Key,
	ops verificationDBOps,
) []verificationCheck {
	if ctx == nil {
		ctx = context.Background()
	}

	checks := make([]verificationCheck, 0)
	masterTarget := verificationTarget{
		AccountRole: "runtime",
		Database:    masterDatabase,
		Operation:   "connect",
	}
	registryTarget := masterTarget
	registryTarget.Operation = "load_enabled_tenants"
	if strings.TrimSpace(masterDSN) == "" {
		checks = append(checks, verificationFailure(
			"master.connection",
			masterTarget,
			verificationErrorConfiguration,
			nil,
		))
		checks = append(checks, newVerificationNotVerified(
			"master.registry",
			registryTarget,
			verificationErrorConfiguration,
		))
		return checks
	}
	if ops.openMaster == nil {
		checks = append(checks, verificationFailure(
			"master.connection",
			masterTarget,
			verificationErrorInternal,
			nil,
		))
		checks = append(checks, newVerificationNotVerified(
			"master.registry",
			registryTarget,
			verificationErrorInternal,
		))
		return checks
	}
	masterCtx, cancel := withVerificationTimeout(ctx)
	masterDB, err := ops.openMaster(masterCtx, masterDSN)
	cancel()
	if err != nil || masterDB == nil {
		if err == nil {
			err = errors.New("master database handle is not configured")
		}
		checks = append(checks, verificationCheckForError(
			"master.connection",
			masterTarget,
			err,
			verificationErrorConnection,
		))
		category, _ := verificationErrorFor(err, verificationErrorConnection)
		checks = append(checks, newVerificationNotVerified("master.registry", registryTarget, category))
		if masterDB != nil {
			checks = appendCloseCheck(checks, ops, masterDB, "master.connection_close", masterTarget)
		}
		return checks
	}
	checks = append(checks, verificationSuccess("master.connection", masterTarget))

	if ops.loadTenants == nil {
		checks = append(checks, verificationFailure(
			"master.registry",
			registryTarget,
			verificationErrorInternal,
			nil,
		))
		return appendCloseCheck(checks, ops, masterDB, "master.connection_close", masterTarget)
	}

	registryCtx, cancel := withVerificationTimeout(ctx)
	tenants, err := ops.loadTenants(registryCtx, masterDB)
	cancel()
	if err != nil {
		checks = append(checks, verificationCheckForError(
			"master.registry",
			registryTarget,
			err,
			verificationErrorRegistry,
		))
		return appendCloseCheck(checks, ops, masterDB, "master.connection_close", masterTarget)
	}
	if len(tenants) == 0 {
		checks = append(checks, verificationFailure(
			"master.registry",
			registryTarget,
			verificationErrorNotReady,
			nil,
		))
		return appendCloseCheck(checks, ops, masterDB, "master.connection_close", masterTarget)
	}
	if err := validateVerificationTenants(tenants); err != nil {
		checks = append(checks, verificationCheckForError(
			"master.registry",
			registryTarget,
			err,
			verificationErrorRegistry,
		))
		return appendCloseCheck(checks, ops, masterDB, "master.connection_close", masterTarget)
	}
	checks = append(checks, verificationSuccess("master.registry", registryTarget))
	checks = appendCloseCheck(checks, ops, masterDB, "master.connection_close", masterTarget)

	sort.Slice(tenants, func(i, j int) bool {
		return tenants[i].ID < tenants[j].ID
	})
	for _, row := range tenants {
		checks = append(checks, verifyTenantDatabase(
			ctx,
			row,
			runtimeBaseDSN,
			onboardingBaseDSN,
			encKey,
			ops,
		)...)
	}
	return checks
}

func validateVerificationTenants(tenants []tenant.Tenant) error {
	if err := tenant.ValidateAll(tenants); err != nil {
		return err
	}
	seenIDs := make(map[int64]struct{}, len(tenants))
	for _, row := range tenants {
		if row.ID <= 0 {
			return errors.New("tenant registry contains an invalid tenant ID")
		}
		if _, exists := seenIDs[row.ID]; exists {
			return errors.New("tenant registry contains duplicate tenant IDs")
		}
		seenIDs[row.ID] = struct{}{}
	}
	return nil
}

func verifyTenantDatabase(
	ctx context.Context,
	row tenant.Tenant,
	runtimeBaseDSN string,
	onboardingBaseDSN string,
	encKey *secutil.Key,
	ops verificationDBOps,
) []verificationCheck {
	checks := make([]verificationCheck, 0)
	runtimeTarget := tenantVerificationTarget(row, "runtime", "connect")

	var runtimeDB *sql.DB
	var err error
	if runtimeBaseDSN == "" {
		err = errors.New("runtime database DSN is not configured")
		checks = append(checks, verificationFailure(
			fmt.Sprintf("tenant.%d.runtime.connection", row.ID),
			runtimeTarget,
			verificationErrorConfiguration,
			nil,
		))
	} else if ops.openTenant == nil {
		checks = append(checks, verificationFailure(
			fmt.Sprintf("tenant.%d.runtime.connection", row.ID),
			runtimeTarget,
			verificationErrorInternal,
			nil,
		))
	} else {
		openCtx, cancel := withVerificationTimeout(ctx)
		runtimeDB, err = ops.openTenant(openCtx, row, runtimeBaseDSN)
		cancel()
		if err != nil || runtimeDB == nil {
			if err == nil {
				err = errors.New("tenant database handle is not configured")
			}
			checks = append(checks, verificationCheckForError(
				fmt.Sprintf("tenant.%d.runtime.connection", row.ID),
				runtimeTarget,
				err,
				verificationErrorConnection,
			))
			if runtimeDB != nil {
				checks = appendCloseCheck(checks, ops, runtimeDB,
					fmt.Sprintf("tenant.%d.runtime.connection_close", row.ID), runtimeTarget)
			}
		} else {
			checks = append(checks, verificationSuccess(
				fmt.Sprintf("tenant.%d.runtime.connection", row.ID),
				runtimeTarget,
			))
		}
	}

	if runtimeDB != nil {
		schemaTarget := tenantVerificationTarget(row, "runtime", "schema_preflight")
		if ops.preflight == nil {
			checks = append(checks, verificationFailure(
				fmt.Sprintf("tenant.%d.schema", row.ID),
				schemaTarget,
				verificationErrorInternal,
				nil,
			))
		} else {
			schemaCtx, cancel := withVerificationTimeout(ctx)
			err := ops.preflight(schemaCtx, runtimeDB)
			cancel()
			checks = append(checks, verificationCheckForError(
				fmt.Sprintf("tenant.%d.schema", row.ID),
				schemaTarget,
				err,
				verificationErrorSchema,
			))
		}

		var branches []int64
		branchTarget := tenantVerificationTarget(row, "runtime", "discover_active_branches")
		if ops.activeBranches == nil {
			checks = append(checks, verificationFailure(
				fmt.Sprintf("tenant.%d.active_branches", row.ID),
				branchTarget,
				verificationErrorInternal,
				nil,
			))
		} else {
			branchCtx, cancel := withVerificationTimeout(ctx)
			branches, err = ops.activeBranches(branchCtx, runtimeDB)
			cancel()
			if err != nil {
				checks = append(checks, verificationCheckForError(
					fmt.Sprintf("tenant.%d.active_branches", row.ID),
					branchTarget,
					err,
					verificationErrorSchema,
				))
				branches = nil
			} else if normalized, normalizeErr := normalizeVerificationBranches(branches); normalizeErr != nil {
				checks = append(checks, verificationCheckForError(
					fmt.Sprintf("tenant.%d.active_branches", row.ID),
					branchTarget,
					normalizeErr,
					verificationErrorNotReady,
				))
				branches = nil
			} else if len(normalized) == 0 {
				checks = append(checks, verificationFailure(
					fmt.Sprintf("tenant.%d.active_branches", row.ID),
					branchTarget,
					verificationErrorNotReady,
					nil,
				))
				branches = normalized
			} else {
				checks = append(checks, verificationSuccess(
					fmt.Sprintf("tenant.%d.active_branches", row.ID),
					branchTarget,
				))
				branches = normalized
			}
		}

		checks = append(checks, verifySQLProbes(ctx, runtimeDB, row, "runtime", ops.runtimeProbes, ops)...)

		if onboardingBaseDSN == "" {
			checks = append(checks, verifySQLProbes(
				ctx,
				runtimeDB,
				row,
				"runtime",
				prefixOnboardingProbeIDs(ops.onboardingProbes, "runtime."),
				ops,
			)...)
		} else {
			checks = append(checks, verifySQLProbes(
				ctx,
				runtimeDB,
				row,
				"runtime",
				runtimeOnboardingReadProbes(ops.onboardingProbes),
				ops,
			)...)
		}

		for _, branchID := range branches {
			branchTarget := tenantVerificationTarget(row, "runtime", "load_and_decrypt_branch_config")
			branchID := branchID
			branchTarget.BranchID = &branchID
			if encKey == nil {
				checks = append(checks, verificationFailure(
					fmt.Sprintf("tenant.%d.branch.%d.config", row.ID, branchID),
					branchTarget,
					verificationErrorConfiguration,
					nil,
				))
				continue
			}
			if ops.loadBranchConfig == nil {
				checks = append(checks, verificationFailure(
					fmt.Sprintf("tenant.%d.branch.%d.config", row.ID, branchID),
					branchTarget,
					verificationErrorInternal,
					nil,
				))
				continue
			}
			configCtx, cancel := withVerificationTimeout(ctx)
			err := ops.loadBranchConfig(configCtx, runtimeDB, branchID, encKey)
			cancel()
			checks = append(checks, verificationCheckForError(
				fmt.Sprintf("tenant.%d.branch.%d.config", row.ID, branchID),
				branchTarget,
				err,
				verificationErrorConnection,
			))
		}

		checks = appendCloseCheck(
			checks,
			ops,
			runtimeDB,
			fmt.Sprintf("tenant.%d.runtime.connection_close", row.ID),
			runtimeTarget,
		)
	} else if onboardingBaseDSN == "" {
		// The runtime account is the onboarding fallback, so a failed runtime
		// connection also leaves those permissions unknown.
	} else {
		checks = append(checks, verifyOnboardingDatabase(ctx, row, onboardingBaseDSN, ops)...)
		return checks
	}

	if onboardingBaseDSN != "" {
		checks = append(checks, verifyOnboardingDatabase(ctx, row, onboardingBaseDSN, ops)...)
	}
	return checks
}

func verifyOnboardingDatabase(
	ctx context.Context,
	row tenant.Tenant,
	onboardingBaseDSN string,
	ops verificationDBOps,
) []verificationCheck {
	checks := make([]verificationCheck, 0)
	target := tenantVerificationTarget(row, "onboarding", "connect")
	checkID := fmt.Sprintf("tenant.%d.onboarding.connection", row.ID)
	if ops.openTenant == nil {
		return append(checks, verificationFailure(checkID, target, verificationErrorInternal, nil))
	}
	openCtx, cancel := withVerificationTimeout(ctx)
	db, err := ops.openTenant(openCtx, row, onboardingBaseDSN)
	cancel()
	if err != nil || db == nil {
		if err == nil {
			err = errors.New("onboarding database handle is not configured")
		}
		checks = append(checks, verificationCheckForError(
			checkID,
			target,
			err,
			verificationErrorConnection,
		))
		if db != nil {
			checks = appendCloseCheck(checks, ops, db,
				fmt.Sprintf("tenant.%d.onboarding.connection_close", row.ID), target)
		}
		return checks
	}
	checks = append(checks, verificationSuccess(checkID, target))
	checks = append(checks, verifySQLProbes(ctx, db, row, "onboarding", ops.onboardingProbes, ops)...)
	return appendCloseCheck(
		checks,
		ops,
		db,
		fmt.Sprintf("tenant.%d.onboarding.connection_close", row.ID),
		target,
	)
}

func normalizeVerificationBranches(branches []int64) ([]int64, error) {
	if len(branches) == 0 {
		return nil, nil
	}
	normalized := append([]int64(nil), branches...)
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i] < normalized[j]
	})
	for i, branchID := range normalized {
		if branchID <= 0 {
			return nil, errors.New("active branch list contains an invalid branch ID")
		}
		if i > 0 && normalized[i-1] == branchID {
			return nil, errors.New("active branch list contains duplicate branch IDs")
		}
	}
	return normalized, nil
}

func runtimeOnboardingReadProbes(probes []verificationSQLProbe) []verificationSQLProbe {
	var readProbes []verificationSQLProbe
	for _, probe := range probes {
		if probe.ID != "onboarding.read_branch" {
			continue
		}
		probe.ID = "runtime." + probe.ID
		readProbes = append(readProbes, probe)
	}
	return readProbes
}

func prefixOnboardingProbeIDs(probes []verificationSQLProbe, prefix string) []verificationSQLProbe {
	prefixed := append([]verificationSQLProbe(nil), probes...)
	for i := range prefixed {
		prefixed[i].ID = prefix + prefixed[i].ID
	}
	return prefixed
}

func verifySQLProbes(
	ctx context.Context,
	db *sql.DB,
	row tenant.Tenant,
	accountRole string,
	probes []verificationSQLProbe,
	ops verificationDBOps,
) []verificationCheck {
	if len(probes) == 0 {
		target := tenantVerificationTarget(row, accountRole, "permission_probes")
		return []verificationCheck{verificationFailure(
			fmt.Sprintf("tenant.%d.%s.permissions", row.ID, accountRole),
			target,
			verificationErrorInternal,
			nil,
		)}
	}

	checks := make([]verificationCheck, 0, len(probes))
	seen := make(map[string]struct{}, len(probes))
	contractFailureAdded := false
	for i, probe := range probes {
		checkID := fmt.Sprintf("tenant.%d.%s", row.ID, probe.ID)
		target := tenantVerificationTarget(row, accountRole, probe.ID)
		if !validVerificationCheckID(probe.ID) {
			checkID = fmt.Sprintf("tenant.%d.%s.invalid_probe_%d", row.ID, accountRole, i+1)
			checks = append(checks, verificationFailure(
				checkID,
				target,
				verificationErrorInternal,
				nil,
			))
			continue
		}
		if _, duplicate := seen[checkID]; duplicate {
			if !contractFailureAdded {
				checks = append(checks, verificationFailure(
					fmt.Sprintf("tenant.%d.%s.probe_contract", row.ID, accountRole),
					tenantVerificationTarget(row, accountRole, "validate_permission_probes"),
					verificationErrorInternal,
					nil,
				))
				contractFailureAdded = true
			}
			continue
		}
		seen[checkID] = struct{}{}
		if strings.TrimSpace(probe.Statement) == "" || db == nil || ops.explain == nil {
			checks = append(checks, verificationFailure(
				checkID,
				target,
				verificationErrorInternal,
				nil,
			))
			continue
		}
		query := "EXPLAIN " + strings.TrimSpace(probe.Statement)
		args := make([]any, strings.Count(probe.Statement, "?"))
		opCtx, cancel := withVerificationTimeout(ctx)
		err := ops.explain(opCtx, db, query, args)
		cancel()
		checks = append(checks, verificationCheckForError(
			checkID,
			target,
			err,
			verificationErrorPermission,
		))
	}
	return checks
}

func executeVerificationExplain(ctx context.Context, db *sql.DB, query string, args []any) error {
	normalizedQuery := strings.ToUpper(strings.TrimSpace(query))
	if !strings.HasPrefix(normalizedQuery, "EXPLAIN ") ||
		strings.HasPrefix(normalizedQuery, "EXPLAIN ANALYZE ") {
		return errors.New("verification permission query must use plain EXPLAIN")
	}
	if db == nil {
		return errors.New("verification database is not configured")
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}

func withVerificationTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, verificationDBOperationTimeout)
}

func tenantVerificationTarget(row tenant.Tenant, accountRole, operation string) verificationTarget {
	return verificationTarget{
		AccountRole: accountRole,
		TenantID:    strconv.FormatInt(row.ID, 10),
		Database:    row.DBName,
		Operation:   operation,
	}
}

func verificationSuccess(id string, target verificationTarget) verificationCheck {
	return verificationCheck{
		ID:     id,
		Status: verificationPass,
		Target: target,
	}
}

func verificationFailure(
	id string,
	target verificationTarget,
	category verificationErrorCategory,
	mysqlCode *int,
) verificationCheck {
	return verificationCheck{
		ID:            id,
		Status:        verificationFail,
		Target:        target,
		ErrorCategory: category,
		MySQLCode:     mysqlCode,
	}
}

func newVerificationNotVerified(
	id string,
	target verificationTarget,
	category verificationErrorCategory,
) verificationCheck {
	return verificationCheck{
		ID:            id,
		Status:        verificationNotVerified,
		Target:        target,
		ErrorCategory: category,
	}
}

func verificationCheckForError(
	id string,
	target verificationTarget,
	err error,
	fallback verificationErrorCategory,
) verificationCheck {
	if err == nil {
		return verificationSuccess(id, target)
	}
	category, mysqlCode := verificationErrorFor(err, fallback)
	return verificationFailure(id, target, category, mysqlCode)
}

func verificationErrorFor(
	err error,
	fallback verificationErrorCategory,
) (verificationErrorCategory, *int) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return verificationErrorTimeout, nil
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		if networkErr.Timeout() {
			return verificationErrorTimeout, nil
		}
		return verificationErrorConnection, nil
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, io.EOF) {
		return verificationErrorConnection, nil
	}
	if errors.Is(err, storeconfig.ErrBranchConfigDecryption) {
		return verificationErrorDecryption, nil
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		code := int(mysqlErr.Number)
		switch mysqlErr.Number {
		case 1044, 1142, 1143, 1227:
			return verificationErrorPermission, &code
		case 1045, 2002, 2003, 2005, 2006, 2013, 2055:
			return verificationErrorConnection, &code
		case 1046, 1049:
			return verificationErrorConfiguration, &code
		case 1051, 1054, 1146, 1932:
			return verificationErrorSchema, &code
		case 1064, 1065:
			return verificationErrorInternal, &code
		}
		if fallback == "" {
			fallback = verificationErrorConnection
		}
		return fallback, &code
	}
	if errors.Is(err, queue.ErrTenantSchemaNotReady) {
		return verificationErrorSchema, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return verificationErrorNotReady, nil
	}
	if fallback == "" {
		fallback = verificationErrorInternal
	}
	return fallback, nil
}

func appendCloseCheck(
	checks []verificationCheck,
	ops verificationDBOps,
	db *sql.DB,
	id string,
	target verificationTarget,
) []verificationCheck {
	if db == nil {
		return checks
	}
	closeDB := ops.closeDB
	if closeDB == nil {
		closeDB = func(db *sql.DB) error {
			return db.Close()
		}
	}
	if err := closeDB(db); err != nil {
		target.Operation = "close_connection"
		checks = append(checks, verificationCheckForError(
			id,
			target,
			err,
			verificationErrorConnection,
		))
	}
	return checks
}

func verificationRuntimeSQLProbes() []verificationSQLProbe {
	return []verificationSQLProbe{
		{
			ID: "runtime.read_bill",
			Statement: `SELECT
				b.id, b.effective_date, b.payment_due_date, b.state, b.discount, b.branch_id,
				b.total_before_vat, b.total_vat, b.total, b.sequence_number, b.merchant_id, b.note,
				b.payment_method, b.userName, b.user_phone_number, b.invoice_uuid,
				c.id, c.name, c.vat_registration_number,
				s.address_name,
				cl.name, cl.company_name, cl.vat_number, cl.address, cl.street, cl.building,
				cl.district, cl.city, cl.postal_code, cl.country, cl.scheme_id, cl.registration_id
			FROM bill b
			JOIN store s ON s.id = b.store_id
			JOIN company c ON c.id = s.company_id
			LEFT JOIN client cl ON cl.id = b.client_id
			WHERE b.id = ?`,
		},
		{
			ID:        "runtime.read_bill_products",
			Statement: `SELECT p.product_id, p.name, p.price, p.quantity, p.vat, p.discount, p.total_before_vat, p.vat_total, p.total_including_vat FROM bill_product p WHERE p.bill_id = ? ORDER BY p.id`,
		},
		{
			ID:        "runtime.read_claim",
			Statement: `SELECT fingerprint, state, lease_expires_at FROM zatca_consumer_claim WHERE pos_id = ? AND bill_request_id = ? FOR UPDATE`,
		},
		{
			ID:        "runtime.read_claim_owner",
			Statement: `SELECT state, event_id FROM zatca_consumer_claim WHERE pos_id = ? AND bill_request_id = ? AND fingerprint = ?`,
		},
		{
			ID:        "runtime.read_chain",
			Statement: `SELECT last_icv, previous_invoice_hash, icv_reservation_key, icv_reservation_token, reserved_icv, reservation_lease_expires_at FROM branch_zatca_config WHERE branch_id = ? FOR UPDATE`,
		},
		{
			ID:        "runtime.lock_bill",
			Statement: `SELECT branch_id, invoice_uuid, invoice_hash FROM bill WHERE id = ? FOR UPDATE`,
		},
		{
			ID:        "runtime.lock_submission",
			Statement: `SELECT id FROM zatca_submission WHERE bill_id = ? FOR UPDATE`,
		},
		{
			ID:        "runtime.read_submission",
			Statement: `SELECT s.bill_id, s.branch_id, s.invoice_icv, s.previous_invoice_hash, s.invoice_uuid, s.invoice_hash, s.invoice_kind, s.signed_xml, b.qr_code, s.outcome_state, s.provider_code, s.rejection_code, s.rejection_msg, s.provider_warning_message FROM zatca_submission s JOIN bill b ON b.id = s.bill_id WHERE s.bill_id = ?`,
		},
		{
			ID:        "runtime.write_claim",
			Statement: `INSERT INTO zatca_consumer_claim (pos_id, bill_request_id, event_id, egs_sequence_id, fingerprint, state, lease_expires_at) VALUES (?, ?, ?, ?, ?, 'processing', ?)`,
		},
		{
			ID:        "runtime.update_claim",
			Statement: `UPDATE zatca_consumer_claim SET event_id = ?, egs_sequence_id = ?, state = 'processing', lease_expires_at = ? WHERE pos_id = ? AND bill_request_id = ?`,
		},
		{
			ID:        "runtime.release_claim",
			Statement: `UPDATE zatca_consumer_claim SET lease_expires_at = NULL WHERE pos_id = ? AND bill_request_id = ? AND event_id = ? AND fingerprint = ? AND state = ?`,
		},
		{
			ID:        "runtime.complete_claim",
			Statement: `UPDATE zatca_consumer_claim SET state = 'completed', lease_expires_at = NULL, completed_at = CURRENT_TIMESTAMP WHERE pos_id = ? AND bill_request_id = ? AND event_id = ? AND fingerprint = ? AND state = 'processing'`,
		},
		{
			ID: "runtime.write_submission",
			Statement: `INSERT INTO zatca_submission
				(bill_id, branch_id, status, outcome_state, invoice_icv, previous_invoice_hash,
				 invoice_uuid, invoice_hash, invoice_kind, signed_xml)
			VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, ?)`,
		},
		{
			ID:        "runtime.update_bill_invoice",
			Statement: `UPDATE bill SET invoice_uuid = ?, invoice_hash = ?, qr_code = ? WHERE id = ? AND branch_id = ?`,
		},
		{
			ID:        "runtime.update_chain",
			Statement: `UPDATE branch_zatca_config SET last_icv = ?, previous_invoice_hash = ?, icv_reservation_key = NULL, icv_reservation_token = NULL, reserved_icv = NULL, reservation_lease_expires_at = NULL WHERE branch_id = ?`,
		},
		{
			ID:        "runtime.update_submission",
			Statement: `UPDATE zatca_submission SET status = ?, outcome_state = ?, provider_code = ?, rejection_code = ?, rejection_msg = ?, provider_warning_message = ?, submitted_at = CURRENT_TIMESTAMP, outcome_at = CURRENT_TIMESTAMP WHERE id = ? AND outcome_state = ?`,
		},
		{
			ID:        "runtime.write_submission_outcome",
			Statement: `INSERT INTO zatca_submission_outcome (submission_id, outcome_state, provider_code, provider_warning_message, observed_at) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		},
		{
			ID:        "runtime.update_bill_state",
			Statement: `UPDATE bill SET state = 3 WHERE id = ?`,
		},
	}
}

func verificationOnboardingSQLProbes() []verificationSQLProbe {
	return []verificationSQLProbe{
		{
			ID: "onboarding.read_branch",
			Statement: `SELECT
				COALESCE(c.name, ''), COALESCE(c.name_ar, ''),
				COALESCE(c.vat_registration_number, ''), COALESCE(c.commercial_registration_number, ''),
				COALESCE(b.name, ''), COALESCE(s.street_name, ''), COALESCE(s.building_number, ''),
				COALESCE(s.district, ''), COALESCE(s.city, ''), COALESCE(s.postal_code, ''),
				COALESCE(s.country, 'SA'), COALESCE(c.business_category, 'Supply activities'),
				COALESCE(bz.csr_org_identifier, ''), COALESCE(bz.csr_org_unit, ''),
				COALESCE(bz.csr_org_name, ''), COALESCE(bz.csr_country, ''),
				COALESCE(bz.csr_location, ''), COALESCE(bz.business_category, ''),
				COALESCE(bz.seller_vat, ''), COALESCE(bz.seller_crn, ''),
				COALESCE(bz.street, ''), COALESCE(bz.building, ''), COALESCE(bz.district, ''),
				COALESCE(bz.postal_code, ''), COALESCE(bz.zatca_csr, ''),
				COALESCE(bz.zatca_private_key, ''), COALESCE(bz.csr_tin, ''),
				COALESCE(bz.csr_computer_number, ''), COALESCE(bz.zatca_compliance_certificate, '')
			FROM branches b
			JOIN company c ON c.id = b.company_id
			LEFT JOIN store s ON s.id = (
				SELECT s2.id FROM store s2 WHERE s2.branch_id = b.id ORDER BY s2.id LIMIT 1
			)
			LEFT JOIN branch_zatca_config bz ON bz.branch_id = b.id
			WHERE b.id = ?`,
		},
		{
			ID: "onboarding.write_csr",
			Statement: `INSERT INTO branch_zatca_config
				(branch_id, csr_org_identifier, csr_org_unit, csr_org_name, csr_country, csr_location,
				 business_category, zatca_csr, zatca_private_key, csr_tin, csr_computer_number)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				csr_org_identifier = VALUES(csr_org_identifier),
				csr_org_unit = VALUES(csr_org_unit),
				csr_org_name = VALUES(csr_org_name),
				csr_country = VALUES(csr_country),
				csr_location = VALUES(csr_location),
				business_category = VALUES(business_category),
				zatca_csr = VALUES(zatca_csr),
				zatca_private_key = VALUES(zatca_private_key),
				csr_tin = VALUES(csr_tin),
				csr_computer_number = VALUES(csr_computer_number)`,
		},
		{
			ID: "onboarding.write_config",
			Statement: `INSERT INTO branch_zatca_config
				(branch_id, csr_org_identifier, csr_org_unit, csr_org_name, csr_country, csr_location,
				 business_category, seller_vat, seller_crn, street, building, district, postal_code,
				 zatca_csr, zatca_private_key, zatca_compliance_certificate, zatca_compliance_secret,
				 zatca_compliance_request_id, zatca_production_username, zatca_production_password,
				 zatca_production_request_id, zatca_registered_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW())
			ON DUPLICATE KEY UPDATE
				csr_org_identifier = VALUES(csr_org_identifier),
				csr_org_unit = VALUES(csr_org_unit),
				csr_org_name = VALUES(csr_org_name),
				csr_country = VALUES(csr_country),
				csr_location = VALUES(csr_location),
				business_category = VALUES(business_category),
				seller_vat = VALUES(seller_vat),
				seller_crn = VALUES(seller_crn),
				street = VALUES(street),
				building = VALUES(building),
				district = VALUES(district),
				postal_code = VALUES(postal_code),
				zatca_csr = VALUES(zatca_csr),
				zatca_private_key = VALUES(zatca_private_key),
				zatca_compliance_certificate = VALUES(zatca_compliance_certificate),
				zatca_compliance_secret = VALUES(zatca_compliance_secret),
				zatca_compliance_request_id = VALUES(zatca_compliance_request_id),
				zatca_production_username = VALUES(zatca_production_username),
				zatca_production_password = VALUES(zatca_production_password),
				zatca_production_request_id = VALUES(zatca_production_request_id),
				zatca_registered_at = NOW()`,
		},
	}
}
