package tenant

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Tenant represents a single tenant (client) from the zatca_master registry.
type Tenant struct {
	ID         int64
	Name       string
	DBName     string
	ZATCAEnv   string // sandbox, simulation, or production
	AlertEmail string
}

// LoadAll returns all enabled tenants from the master database.
func LoadAll(masterDB *sql.DB) ([]Tenant, error) {
	rows, err := masterDB.Query(`SELECT id, name, db_name, zatca_env, alert_email FROM tenant WHERE enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("query tenants: %w", err)
	}
	defer rows.Close()

	var tenants []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.DBName, &t.ZATCAEnv, &t.AlertEmail); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	return tenants, rows.Err()
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
