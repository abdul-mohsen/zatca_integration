package tenant

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

func TestValidateAllRejectsAmbiguousRouting(t *testing.T) {
	tests := map[string][]Tenant{
		"missing key": {{ID: 1, DBName: "acme"}},
		"duplicate key": {
			{ID: 1, TenantKey: "acme", DBName: "acme_db"},
			{ID: 2, TenantKey: "acme", DBName: "other_db"},
		},
		"duplicate database": {
			{ID: 1, TenantKey: "acme", DBName: "shared_db"},
			{ID: 2, TenantKey: "other", DBName: "shared_db"},
		},
	}
	for name, tenants := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAll(tenants); err == nil {
				t.Fatal("ValidateAll accepted an ambiguous tenant registry")
			}
		})
	}
}

func TestValidateAllAcceptsDistinctTenantIdentityAndRouting(t *testing.T) {
	tenants := []Tenant{
		{ID: 1, TenantKey: "acme-0001", DBName: "acme_db"},
		{ID: 2, TenantKey: "other-0001", DBName: "other_db"},
	}
	if err := ValidateAll(tenants); err != nil {
		t.Fatalf("ValidateAll rejected a valid registry: %v", err)
	}
}

func TestValidateRejectsOversizedIdentity(t *testing.T) {
	for _, field := range []string{"tenant_key", "db_name"} {
		t.Run(field, func(t *testing.T) {
			tenant := Tenant{TenantKey: "acme", DBName: "acme_db"}
			if field == "tenant_key" {
				tenant.TenantKey = strings.Repeat("x", 65)
			} else {
				tenant.DBName = strings.Repeat("x", 65)
			}
			if err := tenant.Validate(); err == nil {
				t.Fatalf("Validate accepted an oversized %s", field)
			}
		})
	}
}

func TestRegistryReconcilesAddedUpdatedAndRemovedTenants(t *testing.T) {
	registry := NewRegistry()
	first := []Tenant{
		{ID: 1, TenantKey: "acme", DBName: "acme_db"},
		{ID: 2, TenantKey: "other", DBName: "other_db"},
	}
	result, err := registry.Reconcile(first)
	if err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if len(result.Added) != 2 || len(result.Updated) != 0 || len(result.Removed) != 0 {
		t.Fatalf("initial result = %+v", result)
	}

	second := []Tenant{
		{ID: 1, TenantKey: "acme", DBName: "acme_db", ZATCAEnv: "simulation"},
		{ID: 3, TenantKey: "new", DBName: "new_db"},
	}
	result, err = registry.Reconcile(second)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(result.Updated) != 1 || result.Updated[0].TenantKey != "acme" {
		t.Fatalf("updated = %+v; want acme", result.Updated)
	}
	if len(result.Added) != 1 || result.Added[0].TenantKey != "new" {
		t.Fatalf("added = %+v; want new", result.Added)
	}
	if len(result.Removed) != 1 || result.Removed[0].TenantKey != "other" {
		t.Fatalf("removed = %+v; want other", result.Removed)
	}
}

func TestValidateDBPrefix(t *testing.T) {
	for _, valid := range []string{"", "tenant_dev_", "tenant_prod_", "tenant_qa_2_", strings.Repeat("a", 63)} {
		if err := ValidateDBPrefix(valid); err != nil {
			t.Errorf("ValidateDBPrefix(%q) = %v; want nil", valid, err)
		}
	}
	for _, invalid := range []string{
		strings.Repeat("a", 64),
		"tenant-dev-",
		"tenant dev",
		"tenant_dev_%",
		"tenant_dev_;DROP",
		"tenant.dev",
	} {
		if err := ValidateDBPrefix(invalid); err == nil {
			t.Errorf("ValidateDBPrefix(%q) = nil; want error", invalid)
		}
	}
}

func TestDBPrefixFromTenantNamePrefix(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "", want: ""},
		{input: "   ", want: ""},
		{input: "dev", want: "tenant_dev_"},
		{input: "dev-", want: "tenant_dev_"},
		{input: "DEV-", want: "tenant_dev_"},
		{input: "prod", want: "tenant_prod_"},
		{input: "prod-", want: "tenant_prod_"},
		{input: "qa-2-", want: "tenant_qa_2_"},
		{input: "qa-2", want: "tenant_qa_2_"},
		{input: "---", wantErr: true},
		{input: "-dev", wantErr: true},
		{input: "dev--2", wantErr: true},
		{input: "dev_2", wantErr: true},
		{input: "dev/2", wantErr: true},
		{input: strings.Repeat("a", 56), wantErr: true}, // "tenant_" (7) + 56 + "_" (1) = 64 > 63
	}
	for _, tc := range tests {
		got, err := DBPrefixFromTenantNamePrefix(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("DBPrefixFromTenantNamePrefix(%q) = (%q, nil); want error", tc.input, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("DBPrefixFromTenantNamePrefix(%q) = (%q, %v); want (%q, nil)", tc.input, got, err, tc.want)
		}
	}
}

func TestResolveDBPrefix(t *testing.T) {
	tests := []struct {
		name             string
		explicitDBPrefix string
		tenantNamePrefix string
		want             string
		wantErr          bool
	}{
		{name: "unscoped", want: ""},
		{name: "explicit only", explicitDBPrefix: "tenant_prod_", want: "tenant_prod_"},
		{name: "derived from name prefix", tenantNamePrefix: "dev-", want: "tenant_dev_"},
		{name: "matching explicit and name prefix", explicitDBPrefix: "tenant_dev_", tenantNamePrefix: "dev-", want: "tenant_dev_"},
		{name: "conflicting prefixes", explicitDBPrefix: "tenant_prod_", tenantNamePrefix: "dev-", wantErr: true},
		{name: "invalid explicit", explicitDBPrefix: "tenant-prod-", wantErr: true},
		{name: "invalid name prefix", tenantNamePrefix: "---", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveDBPrefix(tc.explicitDBPrefix, tc.tenantNamePrefix)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveDBPrefix(%q, %q) = (%q, nil); want error", tc.explicitDBPrefix, tc.tenantNamePrefix, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ResolveDBPrefix(%q, %q) = (%q, %v); want (%q, nil)", tc.explicitDBPrefix, tc.tenantNamePrefix, got, err, tc.want)
			}
		})
	}
}

var fakeTenantDriverSeq atomic.Int64

type fakeTenantQueryCapture struct {
	queries []string
	args    [][]driver.Value
	rows    [][]driver.Value
}

type fakeTenantDriver struct {
	capture *fakeTenantQueryCapture
}

func (d *fakeTenantDriver) Open(string) (driver.Conn, error) {
	return &fakeTenantConn{capture: d.capture}, nil
}

type fakeTenantConn struct {
	capture *fakeTenantQueryCapture
}

func (c *fakeTenantConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (c *fakeTenantConn) Close() error              { return nil }
func (c *fakeTenantConn) Begin() (driver.Tx, error) { return nil, errors.New("not implemented") }

func (c *fakeTenantConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.capture.queries = append(c.capture.queries, query)
	vals := make([]driver.Value, len(args))
	for i, arg := range args {
		vals[i] = arg.Value
	}
	c.capture.args = append(c.capture.args, vals)
	return &fakeTenantRows{rows: c.capture.rows}, nil
}

type fakeTenantRows struct {
	rows [][]driver.Value
	idx  int
}

func (r *fakeTenantRows) Columns() []string {
	return []string{"id", "tenant_key", "name", "db_name", "zatca_env", "alert_email"}
}
func (r *fakeTenantRows) Close() error { return nil }
func (r *fakeTenantRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func openFakeMasterDB(t *testing.T, capture *fakeTenantQueryCapture) *sql.DB {
	t.Helper()
	driverName := fmt.Sprintf("fake_tenant_driver_%d", fakeTenantDriverSeq.Add(1))
	sql.Register(driverName, &fakeTenantDriver{capture: capture})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open(%s): %v", driverName, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestLoadAllWithPrefixContextScopesQueryAndIgnoresOutOfScopeRows(t *testing.T) {
	capture := &fakeTenantQueryCapture{
		rows: [][]driver.Value{
			{int64(1), "dev-acme", "Dev Acme", "tenant_dev_acme", "sandbox", "ops@example.com"},
			// Simulate case-insensitive MySQL collation or out-of-scope row with invalid tenant_key.
			{int64(2), "", "Upper Case", "TENANT_DEV_upper", "sandbox", ""},
			{int64(3), "", "Broken Legacy", "tenant_anime", "sandbox", ""},
		},
	}
	db := openFakeMasterDB(t, capture)

	tenants, err := LoadAllWithPrefixContext(context.Background(), db, "tenant_dev_")
	if err != nil {
		t.Fatalf("LoadAllWithPrefixContext() error = %v", err)
	}
	if len(tenants) != 1 || tenants[0].DBName != "tenant_dev_acme" {
		t.Fatalf("tenants = %+v; want only tenant_dev_acme", tenants)
	}
	if len(capture.queries) != 1 || !strings.Contains(capture.queries[0], "LEFT(db_name, CHAR_LENGTH(?)) = ?") {
		t.Fatalf("queries = %v; want LEFT(db_name, CHAR_LENGTH(?)) = ?", capture.queries)
	}
	if len(capture.args) != 1 || len(capture.args[0]) != 2 || capture.args[0][0] != "tenant_dev_" || capture.args[0][1] != "tenant_dev_" {
		t.Fatalf("args = %v; want [tenant_dev_ tenant_dev_]", capture.args)
	}
}

func TestLoadAllWithPrefixContextRejectsInvalidPrefixBeforeQuerying(t *testing.T) {
	capture := &fakeTenantQueryCapture{}
	db := openFakeMasterDB(t, capture)

	if _, err := LoadAllWithPrefixContext(context.Background(), db, "tenant-dev-"); err == nil {
		t.Fatal("LoadAllWithPrefixContext accepted an invalid prefix")
	}
	if len(capture.queries) != 0 {
		t.Fatalf("queries = %v; want no SQL query on invalid prefix", capture.queries)
	}
}
