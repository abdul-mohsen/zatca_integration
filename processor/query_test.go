package processor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

var replayQueryDriverID uint64

func TestQueryPendingBillsExcludesDrafts(t *testing.T) {
	driverName := fmt.Sprintf("replay_query_%d", atomic.AddUint64(&replayQueryDriverID, 1))
	sql.Register(driverName, replayQueryTestDriver{})

	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer db.Close()

	ids, err := QueryPendingBills(db, 7)
	if err != nil {
		t.Fatalf("QueryPendingBills() error = %v", err)
	}

	want := []int64{2}
	if len(ids) != len(want) {
		t.Fatalf("QueryPendingBills() = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("QueryPendingBills() = %v, want %v", ids, want)
		}
	}
}

type replayQueryTestDriver struct{}

func (replayQueryTestDriver) Open(string) (driver.Conn, error) {
	return replayQueryTestConn{}, nil
}

type replayQueryTestConn struct{}

func (replayQueryTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (replayQueryTestConn) Close() error {
	return nil
}

func (replayQueryTestConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func (replayQueryTestConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "state < 3"):
		return &replayQueryTestRows{
			values: [][]driver.Value{{int64(1)}, {int64(2)}},
		}, nil
	case strings.Contains(query, "state = 1"):
		return &replayQueryTestRows{
			values: [][]driver.Value{{int64(2)}},
		}, nil
	default:
		return nil, fmt.Errorf("unexpected replay query: %s", query)
	}
}

type replayQueryTestRows struct {
	values [][]driver.Value
	index  int
}

func (replayQueryTestRows) Columns() []string {
	return []string{"id"}
}

func (replayQueryTestRows) Close() error {
	return nil
}

func (r *replayQueryTestRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
