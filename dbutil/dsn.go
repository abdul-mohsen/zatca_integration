package dbutil

import (
	"fmt"
	"net"

	"github.com/go-sql-driver/mysql"
)

// FormatDSN builds a MySQL DSN that safely handles special characters in passwords.
func FormatDSN(user, pass, host, port, dbName string) string {
	cfg := mysql.NewConfig()
	cfg.User = user
	cfg.Passwd = pass
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, port)
	cfg.DBName = dbName
	return cfg.FormatDSN()
}

// FormatBaseDSN builds a base DSN (no database name) for use with tenant.OpenDB.
func FormatBaseDSN(user, pass, host, port string) string {
	return FormatDSN(user, pass, host, port, "")
}

// ResolveDSN returns a DSN string from either an explicit dsn value or individual parts.
// If dsn is non-empty it is returned as-is. Otherwise it constructs one from the parts.
func ResolveDSN(dsn, user, pass, host, port, dbName string) (string, error) {
	if dsn != "" {
		return dsn, nil
	}
	var missing []string
	if host == "" {
		missing = append(missing, "db-host/HOST")
	}
	if port == "" {
		missing = append(missing, "db-port/PORT")
	}
	if user == "" {
		missing = append(missing, "db-user/DBUSER")
	}
	if dbName == "" {
		missing = append(missing, "db-name/DBNAME")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("missing required DB parameters: %v", missing)
	}
	return FormatDSN(user, pass, host, port, dbName), nil
}
