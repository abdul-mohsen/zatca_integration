package tenant

import (
	"strings"
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
