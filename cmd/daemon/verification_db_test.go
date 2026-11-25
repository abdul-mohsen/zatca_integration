package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/secutil"
	"github.com/zatca-go/zatca/storeconfig"
	"github.com/zatca-go/zatca/tenant"
)

func TestVerifyDatabaseCoversMasterEveryTenantAndActiveBranch(t *testing.T) {
	first := verificationTestTenant(1, "tenant_one")
	second := verificationTestTenant(2, "tenant_two")
	state := &verificationDBTestState{
		tenants: []tenant.Tenant{first, second},
		branches: map[int64][]int64{
			first.ID:  {11},
			second.ID: {21, 22},
		},
	}
	ops := newVerificationDBTestOps(t, state)

	checks := verifyDatabase(
		context.Background(),
		"master-dsn",
		"zatca_master",
		"runtime-base-dsn",
		"",
		verificationTestKey(t),
		ops,
	)

	assertVerificationStatus(t, checks, "master.connection", verificationPass)
	assertVerificationStatus(t, checks, "master.registry", verificationPass)
	for _, tenantID := range []int64{first.ID, second.ID} {
		prefix := fmt.Sprintf("tenant.%d.", tenantID)
		assertVerificationStatus(t, checks, prefix+"runtime.connection", verificationPass)
		assertVerificationStatus(t, checks, prefix+"schema", verificationPass)
		assertVerificationStatus(t, checks, prefix+"active_branches", verificationPass)
		assertVerificationStatus(t, checks, prefix+"runtime.read_bill", verificationPass)
		assertVerificationStatus(t, checks, prefix+"runtime.write_submission", verificationPass)
		assertVerificationStatus(t, checks, prefix+"runtime.onboarding.read_branch", verificationPass)
		assertVerificationStatus(t, checks, prefix+"runtime.onboarding.write_config", verificationPass)
	}
	for _, branchID := range []int64{11, 21, 22} {
		id := fmt.Sprintf("tenant.%d.branch.%d.config", branchTenantID(branchID, first, second), branchID)
		assertVerificationStatus(t, checks, id, verificationPass)
	}

	report, err := buildVerificationReport(checks)
	if err != nil {
		t.Fatalf("buildVerificationReport() error = %v", err)
	}
	if report.OverallStatus != verificationPass {
		t.Fatalf("overall_status = %q; want PASS", report.OverallStatus)
	}
	if len(state.branchConfigCalls) != 3 {
		t.Fatalf("branch config calls = %v; want one call for each active branch", state.branchConfigCalls)
	}
	for _, call := range state.explainCalls {
		if !strings.HasPrefix(call.query, "EXPLAIN ") {
			t.Errorf("permission probe was not plain EXPLAIN: %q", call.query)
		}
	}
}

func TestVerifyDatabaseContinuesAfterTenantSchemaAndDecryptionFailures(t *testing.T) {
	first := verificationTestTenant(1, "tenant_one")
	second := verificationTestTenant(2, "tenant_two")
	state := &verificationDBTestState{
		tenants: []tenant.Tenant{first, second},
		openErrors: map[string]error{
			"runtime:1": errors.New("connection failed with DB_PASSWORD_FIXTURE"),
		},
		schemaErrors: map[int64]error{
			second.ID: fmt.Errorf("%w: fixture schema error", queue.ErrTenantSchemaNotReady),
		},
		branches: map[int64][]int64{
			second.ID: {201, 202},
		},
		branchErrors: map[string]error{
			"2:201": storeconfig.ErrBranchConfigDecryption,
		},
	}
	ops := newVerificationDBTestOps(t, state)

	checks := verifyDatabase(
		context.Background(),
		"master-dsn",
		"zatca_master",
		"runtime-base-dsn",
		"",
		verificationTestKey(t),
		ops,
	)

	assertVerificationStatus(t, checks, "tenant.1.runtime.connection", verificationFail)
	assertVerificationStatus(t, checks, "tenant.2.schema", verificationFail)
	assertVerificationStatus(t, checks, "tenant.2.branch.201.config", verificationFail)
	assertVerificationStatus(t, checks, "tenant.2.branch.202.config", verificationPass)
	assertVerificationStatus(t, checks, "tenant.2.runtime.read_bill", verificationPass)

	report, err := buildVerificationReport(checks)
	if err != nil {
		t.Fatalf("buildVerificationReport() error = %v", err)
	}
	if report.OverallStatus != verificationFail {
		t.Fatalf("overall_status = %q; want FAIL", report.OverallStatus)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "DB_PASSWORD_FIXTURE") {
		t.Fatalf("report exposed a driver diagnostic: %s", encoded)
	}
	if len(state.branchConfigCalls) != 2 {
		t.Fatalf("branch config calls = %v; want both active branches attempted", state.branchConfigCalls)
	}
}

func TestVerifyDatabaseRejectsEmptyRegistryAndBranchSet(t *testing.T) {
	t.Run("empty registry", func(t *testing.T) {
		state := &verificationDBTestState{}
		ops := newVerificationDBTestOps(t, state)
		checks := verifyDatabase(
			context.Background(),
			"master-dsn",
			"zatca_master",
			"runtime-base-dsn",
			"",
			verificationTestKey(t),
			ops,
		)
		check := requireVerificationCheck(t, checks, "master.registry")
		if check.Status != verificationFail || check.ErrorCategory != verificationErrorNotReady {
			t.Fatalf("empty registry check = %+v; want not-ready failure", check)
		}
	})

	t.Run("empty active branches", func(t *testing.T) {
		tenantRow := verificationTestTenant(3, "tenant_three")
		state := &verificationDBTestState{tenants: []tenant.Tenant{tenantRow}}
		ops := newVerificationDBTestOps(t, state)
		checks := verifyDatabase(
			context.Background(),
			"master-dsn",
			"zatca_master",
			"runtime-base-dsn",
			"",
			verificationTestKey(t),
			ops,
		)
		check := requireVerificationCheck(t, checks, "tenant.3.active_branches")
		if check.Status != verificationFail || check.ErrorCategory != verificationErrorNotReady {
			t.Fatalf("empty branch check = %+v; want not-ready failure", check)
		}
	})
}

func TestVerifyDatabaseUsesSeparateOnboardingAccountWhenConfigured(t *testing.T) {
	tenantRow := verificationTestTenant(4, "tenant_four")
	state := &verificationDBTestState{
		tenants:  []tenant.Tenant{tenantRow},
		branches: map[int64][]int64{tenantRow.ID: {41}},
	}
	ops := newVerificationDBTestOps(t, state)

	checks := verifyDatabase(
		context.Background(),
		"master-dsn",
		"zatca_master",
		"runtime-base-dsn",
		"onboarding-base-dsn",
		verificationTestKey(t),
		ops,
	)

	onboardingCheck := requireVerificationCheck(t, checks, "tenant.4.onboarding.write_config")
	if onboardingCheck.Status != verificationPass {
		t.Fatalf("onboarding probe = %+v; want PASS", onboardingCheck)
	}
	if onboardingCheck.Target.AccountRole != "onboarding" {
		t.Fatalf("onboarding account_role = %q; want onboarding", onboardingCheck.Target.AccountRole)
	}
	if !containsString(state.openedDBs, "onboarding:4") {
		t.Fatalf("separate onboarding DB was not opened: %v", state.openedDBs)
	}
}

func TestVerifyDatabaseReportsOnboardingFallbackFailure(t *testing.T) {
	tenantRow := verificationTestTenant(5, "tenant_five")
	state := &verificationDBTestState{
		tenants:  []tenant.Tenant{tenantRow},
		branches: map[int64][]int64{tenantRow.ID: {51}},
		openErrors: map[string]error{
			"onboarding:5": errors.New("onboarding connection fixture failure"),
		},
	}
	ops := newVerificationDBTestOps(t, state)

	checks := verifyDatabase(
		context.Background(),
		"master-dsn",
		"zatca_master",
		"runtime-base-dsn",
		"onboarding-base-dsn",
		verificationTestKey(t),
		ops,
	)

	assertVerificationStatus(t, checks, "tenant.5.onboarding.connection", verificationFail)
	assertVerificationStatus(t, checks, "tenant.5.runtime.read_bill", verificationPass)
	if containsVerificationCheck(checks, "tenant.5.onboarding.write_config") {
		t.Fatal("onboarding probe ran without a verified onboarding connection")
	}
}

func TestVerifyDatabaseBoundsOperationsAndClassifiesTimeouts(t *testing.T) {
	state := &verificationDBTestState{}
	ops := newVerificationDBTestOps(t, state)
	deadlineSeen := false
	ops.loadTenants = func(ctx context.Context, _ *sql.DB) ([]tenant.Tenant, error) {
		_, deadlineSeen = ctx.Deadline()
		return nil, context.DeadlineExceeded
	}

	checks := verifyDatabase(
		context.Background(),
		"master-dsn",
		"zatca_master",
		"runtime-base-dsn",
		"",
		verificationTestKey(t),
		ops,
	)

	if !deadlineSeen {
		t.Fatal("tenant registry query did not receive a bounded context")
	}
	check := requireVerificationCheck(t, checks, "master.registry")
	if check.Status != verificationFail || check.ErrorCategory != verificationErrorTimeout {
		t.Fatalf("registry timeout check = %+v; want timeout failure", check)
	}
}

func TestVerificationSQLProbesUsePlainExplainAndContinue(t *testing.T) {
	tenantRow := verificationTestTenant(6, "tenant_six")
	state := &verificationDBTestState{}
	ops := newVerificationDBTestOps(t, state)
	state.explainErrors = map[string]error{
		"EXPLAIN INSERT INTO zatca_submission (bill_id, branch_id) VALUES (?, ?)": &mysql.MySQLError{Number: 1142, Message: "permission denied"},
	}
	probes := []verificationSQLProbe{
		{
			ID:        "runtime.read_bill",
			Statement: "SELECT id FROM bill WHERE id = ?",
		},
		{
			ID:        "runtime.write_submission",
			Statement: "INSERT INTO zatca_submission (bill_id, branch_id) VALUES (?, ?)",
		},
	}

	checks := verifySQLProbes(
		context.Background(),
		newVerificationOpaqueDB(t),
		tenantRow,
		"runtime",
		probes,
		ops,
	)

	if len(state.explainCalls) != len(probes) {
		t.Fatalf("EXPLAIN calls = %d; want %d", len(state.explainCalls), len(probes))
	}
	for i, call := range state.explainCalls {
		if !strings.HasPrefix(call.query, "EXPLAIN ") || strings.HasPrefix(call.query, "EXPLAIN ANALYZE") {
			t.Errorf("probe %d did not use plain EXPLAIN: %q", i, call.query)
		}
		if len(call.args) != strings.Count(probes[i].Statement, "?") {
			t.Errorf("probe %d argument count = %d; want %d", i, len(call.args), strings.Count(probes[i].Statement, "?"))
		}
		for _, arg := range call.args {
			if arg != nil {
				t.Errorf("probe %d used a non-empty argument value: %#v", i, arg)
			}
		}
	}

	assertVerificationStatus(t, checks, "tenant.6.runtime.read_bill", verificationPass)
	failed := requireVerificationCheck(t, checks, "tenant.6.runtime.write_submission")
	if failed.Status != verificationFail ||
		failed.ErrorCategory != verificationErrorPermission ||
		failed.MySQLCode == nil ||
		*failed.MySQLCode != 1142 {
		t.Fatalf("write permission result = %+v; want safe MySQL permission error", failed)
	}
}

func TestExecuteVerificationExplainUsesQueryAndNeverExecutesStatement(t *testing.T) {
	state := &verificationExplainTestState{}
	db := sql.OpenDB(verificationExplainTestConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })

	query := "EXPLAIN INSERT INTO zatca_submission (bill_id) VALUES (?)"
	if err := executeVerificationExplain(context.Background(), db, query, []any{nil}); err != nil {
		t.Fatalf("executeVerificationExplain() error = %v", err)
	}
	if !reflect.DeepEqual(state.queries, []string{query}) {
		t.Fatalf("queries = %q; want only %q", state.queries, query)
	}
	if len(state.execs) != 0 {
		t.Fatalf("ExecContext was called for a permission probe: %q", state.execs)
	}
}

func TestVerificationReportDoesNotSerializeDatabaseErrorText(t *testing.T) {
	state := &verificationDBTestState{
		masterError: errors.New("connection failed with DB_PASSWORD_FIXTURE and dsn:DSN_FIXTURE"),
	}
	ops := newVerificationDBTestOps(t, state)

	checks := verifyDatabase(
		context.Background(),
		"dsn:DB_PASSWORD_FIXTURE",
		"zatca_master",
		"runtime-base-dsn",
		"",
		verificationTestKey(t),
		ops,
	)
	report, err := buildVerificationReport(checks)
	if err != nil {
		t.Fatalf("buildVerificationReport() error = %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, forbidden := range []string{"DB_PASSWORD_FIXTURE", "DSN_FIXTURE", "connection failed"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("report contains raw database error %q: %s", forbidden, encoded)
		}
	}
}

type verificationDBTestState struct {
	tenants           []tenant.Tenant
	masterError       error
	openErrors        map[string]error
	schemaErrors      map[int64]error
	branchQueryError  map[int64]error
	branches          map[int64][]int64
	branchErrors      map[string]error
	explainErrors     map[string]error
	openedDBs         []string
	databaseTenants   map[*sql.DB]int64
	databaseRoles     map[*sql.DB]string
	branchConfigCalls []string
	explainCalls      []verificationExplainCall
}

func newVerificationDBTestOps(t *testing.T, state *verificationDBTestState) verificationDBOps {
	t.Helper()
	if state.openErrors == nil {
		state.openErrors = make(map[string]error)
	}
	if state.schemaErrors == nil {
		state.schemaErrors = make(map[int64]error)
	}
	if state.branchQueryError == nil {
		state.branchQueryError = make(map[int64]error)
	}
	if state.branches == nil {
		state.branches = make(map[int64][]int64)
	}
	if state.branchErrors == nil {
		state.branchErrors = make(map[string]error)
	}
	if state.explainErrors == nil {
		state.explainErrors = make(map[string]error)
	}
	if state.databaseTenants == nil {
		state.databaseTenants = make(map[*sql.DB]int64)
	}
	if state.databaseRoles == nil {
		state.databaseRoles = make(map[*sql.DB]string)
	}

	return verificationDBOps{
		openMaster: func(ctx context.Context, dsn string) (*sql.DB, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("master open did not receive a bounded context")
			}
			if state.masterError != nil {
				return nil, state.masterError
			}
			state.openedDBs = append(state.openedDBs, "master:"+dsn)
			return newVerificationOpaqueDB(t), nil
		},
		openTenant: func(ctx context.Context, row tenant.Tenant, dsn string) (*sql.DB, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("tenant open did not receive a bounded context")
			}
			role := "runtime"
			if dsn == "onboarding-base-dsn" {
				role = "onboarding"
			}
			key := fmt.Sprintf("%s:%d", role, row.ID)
			if err := state.openErrors[key]; err != nil {
				return nil, err
			}
			state.openedDBs = append(state.openedDBs, key)
			db := newVerificationOpaqueDB(t)
			state.databaseTenants[db] = row.ID
			state.databaseRoles[db] = role
			return db, nil
		},
		loadTenants: func(ctx context.Context, _ *sql.DB) ([]tenant.Tenant, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("registry query did not receive a bounded context")
			}
			return append([]tenant.Tenant(nil), state.tenants...), nil
		},
		preflight: func(ctx context.Context, db *sql.DB) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("schema preflight did not receive a bounded context")
			}
			return state.schemaErrors[state.databaseTenants[db]]
		},
		activeBranches: func(ctx context.Context, db *sql.DB) ([]int64, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("branch query did not receive a bounded context")
			}
			tenantID := state.databaseTenants[db]
			return append([]int64(nil), state.branches[tenantID]...), state.branchQueryError[tenantID]
		},
		loadBranchConfig: func(ctx context.Context, db *sql.DB, branchID int64, _ *secutil.Key) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("branch config read did not receive a bounded context")
			}
			tenantID := state.databaseTenants[db]
			state.branchConfigCalls = append(state.branchConfigCalls, fmt.Sprintf("%d:%d", tenantID, branchID))
			return state.branchErrors[fmt.Sprintf("%d:%d", tenantID, branchID)]
		},
		explain: func(ctx context.Context, _ *sql.DB, query string, args []any) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("EXPLAIN query did not receive a bounded context")
			}
			state.explainCalls = append(state.explainCalls, verificationExplainCall{
				query: query,
				args:  append([]any(nil), args...),
			})
			return state.explainErrors[query]
		},
		closeDB: func(db *sql.DB) error {
			if db == nil {
				return nil
			}
			return db.Close()
		},
		runtimeProbes: []verificationSQLProbe{
			{ID: "runtime.read_bill", Statement: "SELECT id FROM bill WHERE id = ?"},
			{ID: "runtime.write_submission", Statement: "INSERT INTO zatca_submission (bill_id) VALUES (?)"},
		},
		onboardingProbes: []verificationSQLProbe{
			{ID: "onboarding.read_branch", Statement: "SELECT branch_id FROM branch_zatca_config WHERE branch_id = ?"},
			{ID: "onboarding.write_config", Statement: "INSERT INTO branch_zatca_config (branch_id) VALUES (?) ON DUPLICATE KEY UPDATE branch_id = VALUES(branch_id)"},
		},
	}
}

func newVerificationOpaqueDB(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(verificationOpaqueConnector{})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func verificationTestTenant(id int64, dbName string) tenant.Tenant {
	return tenant.Tenant{
		ID:        id,
		TenantKey: fmt.Sprintf("tenant-%d", id),
		Name:      fmt.Sprintf("Tenant %d", id),
		DBName:    dbName,
		ZATCAEnv:  "sandbox",
	}
}

func verificationTestKey(t *testing.T) *secutil.Key {
	t.Helper()
	key, err := secutil.NewKey(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("secutil.NewKey() error = %v", err)
	}
	return key
}

func assertVerificationStatus(t *testing.T, checks []verificationCheck, id string, want verificationStatus) {
	t.Helper()
	got := requireVerificationCheck(t, checks, id)
	if got.Status != want {
		t.Fatalf("%s status = %q; want %q", id, got.Status, want)
	}
}

func requireVerificationCheck(t *testing.T, checks []verificationCheck, id string) verificationCheck {
	t.Helper()
	for _, check := range checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("verification check %q is missing", id)
	return verificationCheck{}
}

func containsVerificationCheck(checks []verificationCheck, id string) bool {
	for _, check := range checks {
		if check.ID == id {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func branchTenantID(branchID int64, tenants ...tenant.Tenant) int64 {
	for _, row := range tenants {
		for _, candidate := range []int64{11, 21, 22} {
			if row.ID == 1 && branchID == candidate && candidate == 11 {
				return row.ID
			}
			if row.ID == 2 && (branchID == 21 || branchID == 22) {
				return row.ID
			}
		}
	}
	return 0
}

type verificationOpaqueConnector struct{}

func (verificationOpaqueConnector) Connect(context.Context) (driver.Conn, error) {
	return verificationOpaqueConn{}, nil
}

func (verificationOpaqueConnector) Driver() driver.Driver {
	return verificationOpaqueDriver{}
}

type verificationOpaqueDriver struct{}

func (verificationOpaqueDriver) Open(string) (driver.Conn, error) {
	return verificationOpaqueConn{}, nil
}

type verificationOpaqueConn struct{}

func (verificationOpaqueConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (verificationOpaqueConn) Close() error { return nil }

func (verificationOpaqueConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

type verificationExplainCall struct {
	query string
	args  []any
}

type verificationExplainTestState struct {
	queries []string
	execs   []string
}

type verificationExplainTestConnector struct {
	state *verificationExplainTestState
}

func (c verificationExplainTestConnector) Connect(context.Context) (driver.Conn, error) {
	return verificationExplainTestConn{state: c.state}, nil
}

func (c verificationExplainTestConnector) Driver() driver.Driver {
	return verificationExplainTestDriver{state: c.state}
}

type verificationExplainTestDriver struct {
	state *verificationExplainTestState
}

func (d verificationExplainTestDriver) Open(string) (driver.Conn, error) {
	return verificationExplainTestConn{state: d.state}, nil
}

type verificationExplainTestConn struct {
	state *verificationExplainTestState
}

func (verificationExplainTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (verificationExplainTestConn) Close() error { return nil }

func (verificationExplainTestConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func (c verificationExplainTestConn) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	c.state.queries = append(c.state.queries, query)
	return verificationExplainTestRows{}, nil
}

func (c verificationExplainTestConn) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	c.state.execs = append(c.state.execs, query)
	return driver.RowsAffected(1), nil
}

type verificationExplainTestRows struct{}

func (verificationExplainTestRows) Columns() []string { return []string{"id"} }

func (verificationExplainTestRows) Close() error { return nil }

func (verificationExplainTestRows) Next([]driver.Value) error { return io.EOF }
