package queue

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	mysqlDuplicateKey uint16 = 1062
	mysqlLockNoWait   uint16 = 3572
)

// isMySQLError reports whether err is the MySQL server error with the given number.
func isMySQLError(err error, number uint16) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == number
}

// nullableTime reads a DATETIME whether or not the connection parses times.
type nullableTime struct {
	time.Time
	Valid bool
}

// Scan implements sql.Scanner.
func (t *nullableTime) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		*t = nullableTime{}
		return nil
	case time.Time:
		*t = nullableTime{Time: typed, Valid: true}
		return nil
	case []byte:
		return t.parse(string(typed))
	case string:
		return t.parse(typed)
	default:
		return fmt.Errorf("unsupported DATETIME value %T", value)
	}
}

func (t *nullableTime) parse(value string) error {
	parsed, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
	}
	if err != nil {
		return fmt.Errorf("invalid DATETIME %q", value)
	}
	*t = nullableTime{Time: parsed, Valid: true}
	return nil
}

// truncate shortens s to at most n characters, without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// nullString returns a valid NullString for a non-empty value, cut to n characters.
func nullString(value string, n int) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: truncate(value, n), Valid: true}
}
