package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/tenant"
)

func TestDaemonReadinessRequiresLiveExpectedBranchConsumer(t *testing.T) {
	nc, js := startTenantRaceJetStream(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	r := newDaemonReconciler(ctx, nil, "", "", nil, nc, js, &wg, time.Second, 1)
	const (
		dbName    = "tenant_acme"
		tenantKey = "acme-0001"
		branchID  = int64(7)
	)
	tc := &tenantCtx{
		tenant:           tenant.Tenant{TenantKey: tenantKey, DBName: dbName},
		expectedBranches: map[int64]struct{}{branchID: {}},
	}
	onboardSub, err := nc.Subscribe(queue.OnboardWildcard(dbName), func(*nats.Msg) {})
	if err != nil {
		t.Fatal(err)
	}
	reconcileSub, err := nc.Subscribe(queue.ReconcileSubject(tenantKey), func(*nats.Msg) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = onboardSub.Unsubscribe()
		_ = reconcileSub.Unsubscribe()
	})

	done := make(chan struct{})
	r.mu.Lock()
	r.lastReconcileAt = time.Now()
	r.tenants[dbName] = tc
	r.caches[dbName] = make(map[int64]*branchEntry)
	r.subs["onboard:"+dbName] = onboardSub
	r.subs["reconcile:"+tenantKey] = reconcileSub
	r.workers[branchKey(dbName, branchID)] = workerHandle{done: done}
	r.mu.Unlock()

	err = r.checkReadiness(ctx)
	if err == nil || !strings.Contains(err.Error(), "JetStream consumer for tenant_acme branch 7") {
		t.Fatalf("readiness error = %v; want the missing branch consumer", err)
	}

	consumer, err := js.PullSubscribe(
		queue.OrderedSubject(dbName, branchID),
		queue.OrderedConsumerName(dbName, branchID),
		nats.AckWait(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Unsubscribe()

	if err := r.checkReadiness(ctx); err != nil {
		t.Fatalf("readiness with the expected live consumer = %v; want healthy", err)
	}

	close(done)
	err = r.checkReadiness(ctx)
	if err == nil || !strings.Contains(err.Error(), "worker for tenant_acme branch 7") {
		t.Fatalf("readiness after worker exit = %v; want the stopped worker", err)
	}
}

func TestReconcileFailureMakesReadinessUnhealthy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	r := newDaemonReconciler(ctx, nil, "", "", nil, nil, nil, &wg, time.Second, 1)
	if err := r.reconcile(ctx); err == nil {
		t.Fatal("reconcile succeeded without a master database")
	}
	err := r.checkReadiness(ctx)
	if err == nil || !strings.Contains(err.Error(), "master database is not configured") {
		t.Fatalf("readiness error = %v; want the reconciliation failure", err)
	}
}

func TestDaemonMonitorHandlerReturnsActionableReadinessFailure(t *testing.T) {
	nextCalled := false
	handler := daemonMonitorHandler(
		func(context.Context) error {
			return errors.New("tenant acme preflight failed: missing required table")
		},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			nextCalled = true
		}),
	)
	req := httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "tenant acme preflight failed") {
		t.Fatalf("body = %q; want the preflight failure", rec.Body.String())
	}
	if nextCalled {
		t.Fatal("health failure was forwarded to the NATS monitor")
	}
}
