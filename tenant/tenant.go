package tenant

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
)

// Tenant represents a single tenant (client) from the zatca_master registry.
type Tenant struct {
	ID         int64
	TenantKey  string // immutable stable identity (zatca_master.tenant.tenant_key)
	Name       string
	DBName     string
	ZATCAEnv   string // sandbox, simulation, or production
	AlertEmail string
}

// Validate checks the stable identity used to route a tenant's messages. The
// daemon must fail closed instead of silently overwriting one tenant in its
// db_name map.
func (t Tenant) Validate() error {
	switch {
	case strings.TrimSpace(t.TenantKey) == "":
		return errors.New("tenant_key is required")
	case utf8.RuneCountInString(t.TenantKey) > 64:
		return errors.New("tenant_key must be at most 64 characters")
	case strings.TrimSpace(t.DBName) == "":
		return errors.New("db_name is required")
	case utf8.RuneCountInString(t.DBName) > 64:
		return errors.New("db_name must be at most 64 characters")
	}
	return nil
}

// ValidateAll checks the registry before any tenant database is opened.
// tenant_key is the stable identity; db_name remains the database routing key.
func ValidateAll(tenants []Tenant) error {
	tenantKeys := make(map[string]struct{}, len(tenants))
	dbNames := make(map[string]struct{}, len(tenants))
	for _, t := range tenants {
		if err := t.Validate(); err != nil {
			return fmt.Errorf("tenant %d (%s): %w", t.ID, t.Name, err)
		}
		if _, exists := tenantKeys[t.TenantKey]; exists {
			return fmt.Errorf("tenant_key %q is used by more than one tenant", t.TenantKey)
		}
		if _, exists := dbNames[t.DBName]; exists {
			return fmt.Errorf("db_name %q is used by more than one tenant", t.DBName)
		}
		tenantKeys[t.TenantKey] = struct{}{}
		dbNames[t.DBName] = struct{}{}
	}
	return nil
}

// LoadAll returns all enabled tenants from the master database.
func LoadAll(masterDB *sql.DB) ([]Tenant, error) {
	rows, err := masterDB.Query(`SELECT id, tenant_key, name, db_name, zatca_env, alert_email FROM tenant WHERE enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("query tenants: %w", err)
	}
	defer rows.Close()

	var tenants []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.TenantKey, &t.Name, &t.DBName, &t.ZATCAEnv, &t.AlertEmail); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := ValidateAll(tenants); err != nil {
		return nil, fmt.Errorf("validate tenants: %w", err)
	}
	return tenants, nil
}

// OpenDB opens a connection to this tenant's database using the base DSN template.
// baseDSN should be a DSN without a database name (from dbutil.FormatBaseDSN or "user:pass@tcp(host:port)/").
func OpenDB(t Tenant, baseDSN string) (*sql.DB, error) {
	// Parse the base DSN so we can safely set the database name
	cfg, err := mysql.ParseDSN(baseDSN)
	if err != nil {
		return nil, fmt.Errorf("parse base DSN: %w", err)
	}
	cfg.DBName = t.DBName
	dsn := cfg.FormatDSN()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open tenant db %s: %w", t.DBName, err)
	}
	// Recycle connections before MySQL/NAT/Docker drops them as idle.
	// Default MySQL wait_timeout is 28800s, but Docker bridge networks and
	// stateful firewalls often drop idle TCP after a few minutes, producing
	// "broken pipe" / "unexpected read from socket" warnings on the next use.
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping tenant db %s: %w", t.DBName, err)
	}
	return db, nil
}
