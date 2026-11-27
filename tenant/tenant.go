package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"

	"github.com/zatca-go/zatca/config"
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
	if !validRegistryIdentity(t.TenantKey) {
		return errors.New("tenant_key is required and must be a safe value of at most 64 characters")
	}
	if !validRegistryIdentity(t.DBName) {
		return errors.New("db_name is required and must be a safe value of at most 64 characters")
	}
	if t.ZATCAEnv != "" {
		if _, err := config.ParseEnv(t.ZATCAEnv); err != nil {
			return err
		}
	}
	return nil
}

func validRegistryIdentity(value string) bool {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value ||
		!utf8.ValidString(value) || utf8.RuneCountInString(value) > 64 {
		return false
	}
	for _, r := range value {
		if r == 0 || r == '.' || r == '*' || r == '>' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
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

// ValidateDBPrefix checks an optional database-name prefix used to scope tenant
// discovery (for example "tenant_dev_" or "tenant_prod_"). An empty prefix
// disables scoping.
func ValidateDBPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if len(prefix) > 63 {
		return errors.New("db prefix must be at most 63 characters")
	}
	for i := 0; i < len(prefix); i++ {
		b := prefix[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' {
			continue
		}
		return errors.New("db prefix must contain only ASCII letters, digits, and underscores")
	}
	return nil
}

// DBPrefixFromTenantNamePrefix maps a deployment tenant name prefix
// (e.g. "dev", "dev-", "prod", "prod-", "qa-2-") to the canonical MySQL
// database prefix ("tenant_dev_", "tenant_prod_", "tenant_qa_2_"). An empty
// input returns an empty prefix (unscoped).
func DBPrefixFromTenantNamePrefix(raw string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return "", nil
	}
	body := strings.TrimSuffix(trimmed, "-")
	if body == "" {
		return "", errors.New("tenant name prefix must contain at least one letter or digit")
	}
	parts := strings.Split(body, "-")
	for _, part := range parts {
		if part == "" {
			return "", errors.New("tenant name prefix must use single hyphens between alphanumeric segments")
		}
		for i := 0; i < len(part); i++ {
			b := part[i]
			if (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') {
				continue
			}
			return "", errors.New("tenant name prefix must contain only ASCII letters, digits, and hyphens")
		}
	}
	prefix := "tenant_" + strings.Join(parts, "_") + "_"
	if err := ValidateDBPrefix(prefix); err != nil {
		return "", fmt.Errorf("derived %w", err)
	}
	return prefix, nil
}

// ResolveDBPrefix resolves the active database-name prefix from explicit
// TENANT_DB_PREFIX and/or TENANT_NAME_PREFIX configuration. It fails closed on
// invalid values or conflicting prefixes.
func ResolveDBPrefix(explicitDBPrefix, tenantNamePrefix string) (string, error) {
	explicit := strings.TrimSpace(explicitDBPrefix)
	if err := ValidateDBPrefix(explicit); err != nil {
		return "", fmt.Errorf("invalid tenant db prefix: %w", err)
	}
	derived, err := DBPrefixFromTenantNamePrefix(tenantNamePrefix)
	if err != nil {
		return "", fmt.Errorf("invalid tenant name prefix: %w", err)
	}
	if explicit != "" && derived != "" && explicit != derived {
		return "", fmt.Errorf(
			"conflicting tenant prefix configuration: tenant db prefix %q does not match tenant name prefix %q (derived %q)",
			explicit,
			strings.TrimSpace(tenantNamePrefix),
			derived,
		)
	}
	if explicit != "" {
		return explicit, nil
	}
	return derived, nil
}

// LoadAll returns all enabled tenants from the master database.
func LoadAll(masterDB *sql.DB) ([]Tenant, error) {
	return LoadAllContext(context.Background(), masterDB)
}

// LoadAllContext returns the enabled tenant registry using the caller's
// cancellation and deadline. Reconciliation uses this form so a stalled
// control-plane database cannot keep the daemon from shutting down.
func LoadAllContext(ctx context.Context, masterDB *sql.DB) ([]Tenant, error) {
	return LoadAllWithPrefixContext(ctx, masterDB, "")
}

// LoadAllWithPrefixContext returns enabled tenants whose db_name starts with
// dbPrefix (or all enabled tenants when dbPrefix is empty). Out-of-scope rows
// are excluded before validation so legacy or other-environment rows cannot
// break discovery for this environment.
func LoadAllWithPrefixContext(ctx context.Context, masterDB *sql.DB, dbPrefix string) ([]Tenant, error) {
	if masterDB == nil {
		return nil, errors.New("master database is not configured")
	}
	if err := ValidateDBPrefix(dbPrefix); err != nil {
		return nil, fmt.Errorf("invalid tenant db prefix: %w", err)
	}
	var (
		rows *sql.Rows
		err  error
	)
	if dbPrefix == "" {
		rows, err = masterDB.QueryContext(ctx, `SELECT id, tenant_key, name, db_name, zatca_env, alert_email FROM tenant WHERE enabled = 1`)
	} else {
		rows, err = masterDB.QueryContext(
			ctx,
			`SELECT id, tenant_key, name, db_name, zatca_env, alert_email FROM tenant WHERE enabled = 1 AND LEFT(db_name, CHAR_LENGTH(?)) = ?`,
			dbPrefix,
			dbPrefix,
		)
	}
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
		if dbPrefix != "" && !strings.HasPrefix(t.DBName, dbPrefix) {
			continue
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

// ReconcileResult describes a registry snapshot transition. It is pure state
// handling so the daemon can open/close databases and workers separately.
type ReconcileResult struct {
	Added     []Tenant
	Removed   []Tenant
	Updated   []Tenant
	Unchanged []Tenant
}

// Registry tracks the last validated tenant snapshot by stable tenant key.
type Registry struct {
	tenants map[string]Tenant
}

// NewRegistry creates an empty tenant registry.
func NewRegistry() *Registry {
	return &Registry{tenants: make(map[string]Tenant)}
}

// Reconcile validates and applies a new master snapshot.
func (r *Registry) Reconcile(next []Tenant) (ReconcileResult, error) {
	if r == nil {
		return ReconcileResult{}, errors.New("tenant registry is not configured")
	}
	if err := ValidateAll(next); err != nil {
		return ReconcileResult{}, err
	}
	if r.tenants == nil {
		r.tenants = make(map[string]Tenant)
	}
	nextByKey := make(map[string]Tenant, len(next))
	var result ReconcileResult
	for _, current := range next {
		nextByKey[current.TenantKey] = current
		previous, exists := r.tenants[current.TenantKey]
		switch {
		case !exists:
			result.Added = append(result.Added, current)
		case previous == current:
			result.Unchanged = append(result.Unchanged, current)
		default:
			result.Updated = append(result.Updated, current)
		}
	}
	for key, previous := range r.tenants {
		if _, exists := nextByKey[key]; !exists {
			result.Removed = append(result.Removed, previous)
		}
	}
	r.tenants = nextByKey
	return result, nil
}

// Snapshot returns the current registry without exposing its backing map.
func (r *Registry) Snapshot() []Tenant {
	if r == nil {
		return nil
	}
	snapshot := make([]Tenant, 0, len(r.tenants))
	for _, t := range r.tenants {
		snapshot = append(snapshot, t)
	}
	return snapshot
}

// OpenDB opens a connection to this tenant's database using the base DSN template.
// baseDSN should be a DSN without a database name (from dbutil.FormatBaseDSN or "user:pass@tcp(host:port)/").
func OpenDB(t Tenant, baseDSN string) (*sql.DB, error) {
	return OpenDBContext(context.Background(), t, baseDSN)
}

// OpenDBContext opens and pings a tenant database using the caller's deadline.
func OpenDBContext(ctx context.Context, t Tenant, baseDSN string) (*sql.DB, error) {
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
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping tenant db %s: %w", t.DBName, err)
	}
	return db, nil
}
