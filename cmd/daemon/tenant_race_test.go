package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/tenant"
)

type tenantRaceWorkerState struct {
	mu   sync.RWMutex
	done <-chan struct{}
}

func (s *tenantRaceWorkerState) setDone(done <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = done
}

func (s *tenantRaceWorkerState) isDone() bool {
	s.mu.RLock()
	done := s.done
	s.mu.RUnlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

type tenantRaceConnector struct {
	state  *tenantRaceWorkerState
	closed chan bool
}

func (c *tenantRaceConnector) Connect(context.Context) (driver.Conn, error) {
	return &tenantRaceConn{state: c.state, closed: c.closed}, nil
}

func (*tenantRaceConnector) Driver() driver.Driver {
	return tenantRaceDriver{}
}

type tenantRaceDriver struct{}

func (tenantRaceDriver) Open(string) (driver.Conn, error) {
	return nil, driver.ErrSkip
}

type tenantRaceConn struct {
	state  *tenantRaceWorkerState
	closed chan bool
}

func (tenantRaceConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (c tenantRaceConn) Close() error {
	select {
	case c.closed <- c.state.isDone():
	default:
	}
	return nil
}

func (tenantRaceConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func (tenantRaceConn) Ping(context.Context) error {
	return nil
}

func startTenantRaceJetStream(t *testing.T) (*nats.Conn, nats.JetStreamContext) {
	t.Helper()
	storeDir, err := os.MkdirTemp(".", ".tenant-removal-race-nats-")
	if err != nil {
		t.Fatal(err)
	}
	cleanupStore := true
	t.Cleanup(func() {
		if cleanupStore {
			_ = os.RemoveAll(storeDir)
		}
	})

	ns, err := server.NewServer(&server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  storeDir,
		NoLog:     true,
		NoSigs:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		ns.Shutdown()
		t.Fatal("embedded JetStream server is not ready")
	}

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		ns.Shutdown()
		t.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		ns.Shutdown()
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:      queue.StreamName,
		Subjects:  []string{queue.SubjectAll},
		Retention: nats.WorkQueuePolicy,
		Storage:   nats.MemoryStorage,
	}); err != nil {
		nc.Close()
		ns.Shutdown()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		nc.Close()
		ns.Shutdown()
		ns.WaitForShutdown()
		cleanupStore = false
		_ = os.RemoveAll(storeDir)
	})
	return nc, js
}

func waitForTenantWorkerFetch(t *testing.T, js nats.JetStreamContext, dbName string, branchID int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := js.ConsumerInfo(queue.StreamName, queue.OrderedConsumerName(dbName, branchID))
		if err == nil && info.NumWaiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not issue a pending fetch: err=%v info=%+v", err, info)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRemoveTenantWaitsForBranchWorkerFetchingBeforeClosingDatabases(t *testing.T) {
	nc, js := startTenantRaceJetStream(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workerState := &tenantRaceWorkerState{}
	closed := make(chan bool, 4)
	runtimeDB := sql.OpenDB(&tenantRaceConnector{state: workerState, closed: closed})
	adminDB := sql.OpenDB(&tenantRaceConnector{state: workerState, closed: closed})
	if err := runtimeDB.Ping(); err != nil {
		t.Fatal(err)
	}
	if err := adminDB.Ping(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	r := newDaemonReconciler(ctx, nil, "", "", nil, nc, js, &wg, time.Second, 1)
	const dbName = "tenant_race"
	const branchID = int64(7)
	tc := &tenantCtx{
		tenant:  tenant.Tenant{TenantKey: "tenant-race", DBName: dbName},
		db:      runtimeDB,
		adminDB: adminDB,
	}
	r.mu.Lock()
	r.tenants[dbName] = tc
	r.caches[dbName] = make(map[int64]*branchEntry)
	r.mu.Unlock()

	r.startBranch(tc, dbName, branchID)
	key := branchKey(dbName, branchID)
	r.mu.RLock()
	worker, ok := r.workers[key]
	r.mu.RUnlock()
	if !ok {
		t.Fatal("branch worker was not registered")
	}
	workerState.setDone(worker.done)
	waitForTenantWorkerFetch(t, js, dbName, branchID)

	defer func() {
		cancel()
		nc.Close()
		select {
		case <-worker.done:
		case <-time.After(5 * time.Second):
			t.Log("worker did not stop during test cleanup")
		}
	}()

	r.removeTenant(dbName)

	select {
	case <-worker.done:
	default:
		t.Fatal("tenant removal returned before the branch worker stopped")
	}
	for range 2 {
		select {
		case closedAfterWorker := <-closed:
			if !closedAfterWorker {
				t.Fatal("tenant removal closed a database before the branch worker stopped")
			}
		case <-time.After(time.Second):
			t.Fatal("tenant removal did not close both tenant databases")
		}
	}
}
