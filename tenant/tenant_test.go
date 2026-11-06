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
