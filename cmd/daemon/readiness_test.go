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
	r.tenantReadiness[dbName] = tenantReconcileStatus{
		tenant:                  tc.tenant,
		branchDiscoveryComplete: true,
	}
	r.tenantReadiness["tenant_broken"] = tenantReconcileStatus{
		tenant:     tenant.Tenant{Name: "tenant_broken", DBName: "tenant_broken"},
		startupErr: errors.New("tenant tenant_broken failed schema preflight"),
	}
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

type daemonReadinessFixture struct {
	ctx context.Context
	r   *daemonReconciler
	nc  *nats.Conn
	js  nats.JetStreamContext
}

func newDaemonReadinessFixture(t *testing.T) *daemonReadinessFixture {
	t.Helper()
	nc, js := startTenantRaceJetStream(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

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
	consumer, err := js.PullSubscribe(
		queue.OrderedSubject(dbName, branchID),
		queue.OrderedConsumerName(dbName, branchID),
		nats.AckWait(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = consumer.Unsubscribe() })

	done := make(chan struct{})
	r.mu.Lock()
	r.lastReconcileAt = time.Now()
	r.tenants[dbName] = tc
	r.caches[dbName] = make(map[int64]*branchEntry)
	r.tenantReadiness[dbName] = tenantReconcileStatus{
		tenant:                  tc.tenant,
		branchDiscoveryComplete: true,
	}
	r.subs["onboard:"+dbName] = onboardSub
	r.subs["reconcile:"+tenantKey] = reconcileSub
	r.workers[branchKey(dbName, branchID)] = workerHandle{done: done}
	r.mu.Unlock()

	return &daemonReadinessFixture{ctx: ctx, r: r, nc: nc, js: js}
}

func TestDaemonReadinessDoesNotHideGlobalFailureWithActiveTenant(t *testing.T) {
	fixture := newDaemonReadinessFixture(t)
	fixture.r.mu.Lock()
	fixture.r.lastGlobalReconcileErr = errors.New("global tenant registry discovery failed")
	fixture.r.mu.Unlock()

	err := fixture.r.checkReadiness(fixture.ctx)
	if err == nil || !strings.Contains(err.Error(), "global tenant registry discovery failed") {
		t.Fatalf("readiness with an active tenant and global discovery failure = %v; want global failure",
			err)
	}
}

func TestDaemonReadinessDoesNotHideFailedBranchDiscoveryBehindHealthyTenant(t *testing.T) {
	fixture := newDaemonReadinessFixture(t)
	const (
		dbName    = "tenant_new"
		tenantKey = "new-0001"
	)
	failedTenant := &tenantCtx{
		tenant:           tenant.Tenant{TenantKey: tenantKey, DBName: dbName},
		expectedBranches: make(map[int64]struct{}),
	}
	onboardSub, err := fixture.nc.Subscribe(queue.OnboardWildcard(dbName), func(*nats.Msg) {})
	if err != nil {
		t.Fatal(err)
	}
	reconcileSub, err := fixture.nc.Subscribe(queue.ReconcileSubject(tenantKey), func(*nats.Msg) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.nc.Flush(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = onboardSub.Unsubscribe()
		_ = reconcileSub.Unsubscribe()
	})
	fixture.r.mu.Lock()
	fixture.r.tenants[dbName] = failedTenant
	fixture.r.tenantReadiness[dbName] = tenantReconcileStatus{
		tenant:             failedTenant.tenant,
		branchDiscoveryErr: errors.New("query active branches: database unavailable"),
	}
	fixture.r.subs["onboard:"+dbName] = onboardSub
	fixture.r.subs["reconcile:"+tenantKey] = reconcileSub
	fixture.r.mu.Unlock()

	err = fixture.r.checkReadiness(fixture.ctx)
	if err == nil || !strings.Contains(err.Error(), dbName) {
		t.Fatalf("readiness with a healthy tenant and failed branch discovery = %v; want %s unready",
			err, dbName)
	}
}

func TestDaemonReadinessReportIncludesDegradedTenantAlongsideHealthyWorker(t *testing.T) {
	fixture := newDaemonReadinessFixture(t)
	fixture.r.mu.Lock()
	fixture.r.tenantReadiness["tenant_broken"] = tenantReconcileStatus{
		tenant:     tenant.Tenant{Name: "broken", DBName: "tenant_broken"},
		startupErr: errors.New("tenant schema preflight failed"),
	}
	fixture.r.mu.Unlock()

	report := fixture.r.readinessReport(fixture.ctx)
	if !report.Ready || report.Status != "degraded" || report.ActiveWorkers != 1 {
		t.Fatalf("readiness report = %+v; want usable degraded state with one healthy worker", report)
	}
	for _, status := range report.Tenants {
		if status.Database == "tenant_broken" {
			if status.Ready || status.Status != "degraded" ||
				!strings.Contains(status.Error, "schema preflight failed") {
				t.Fatalf("broken tenant status = %+v; want explicit degraded error", status)
			}
			return
		}
	}
	t.Fatalf("readiness report = %+v; missing tenant_broken status", report)
}

func TestDaemonReadinessRequiresAnActiveTenantWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	r := newDaemonReconciler(ctx, nil, "", "", nil, nil, nil, &wg, time.Second, 1)
	r.mu.Lock()
	r.lastReconcileAt = time.Now()
	r.mu.Unlock()

	if err := r.checkReadiness(ctx); err == nil {
		t.Fatal("readiness succeeded with no tenants, workers, or reconciliation error")
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
		func(context.Context) daemonReadinessReport {
			return daemonReadinessReport{
				Status:      "unhealthy",
				Ready:       false,
				GlobalError: "tenant acme preflight failed: missing required table",
				Error:       "tenant acme preflight failed: missing required table",
			}
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

func TestDaemonMonitorHandlerReturnsPerTenantReadinessReport(t *testing.T) {
	nextCalled := false
	handler := daemonMonitorHandler(
		func(context.Context) daemonReadinessReport {
			return daemonReadinessReport{
				Status:        "degraded",
				Ready:         true,
				ActiveWorkers: 1,
				Tenants: []tenantReadinessReport{{
					Tenant:          "tenant_broken",
					Database:        "tenant_broken",
					Status:          "degraded",
					Ready:           false,
					BranchDiscovery: "pending",
					Error:           "tenant schema preflight failed",
				}},
			}
		},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			nextCalled = true
		}),
	)
	req := httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d for a healthy daemon with a degraded tenant",
			rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), `"tenant":"tenant_broken"`) ||
		!strings.Contains(rec.Body.String(), `"status":"degraded"`) {
		t.Fatalf("body = %q; want per-tenant degraded readiness", rec.Body.String())
	}
	if nextCalled {
		t.Fatal("readiness report was forwarded to the NATS monitor")
	}
}

func TestDaemonReadinessAllowsPreOnboardingTenantWithZeroBranches(t *testing.T) {
	nc, js := startTenantRaceJetStream(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	r := newDaemonReconciler(ctx, nil, "", "", nil, nc, js, &wg, time.Second, 1)
	const (
		dbName    = "tenant_dev_acme"
		tenantKey = "dev-acme"
	)
	tc := &tenantCtx{
		tenant:           tenant.Tenant{Name: "Dev Acme", TenantKey: tenantKey, DBName: dbName},
		expectedBranches: make(map[int64]struct{}),
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

	r.mu.Lock()
	r.lastReconcileAt = time.Now()
	r.tenants[dbName] = tc
	r.caches[dbName] = make(map[int64]*branchEntry)
	r.tenantReadiness[dbName] = tenantReconcileStatus{
		tenant:                  tc.tenant,
		branchDiscoveryComplete: true,
	}
	r.subs["onboard:"+dbName] = onboardSub
	r.subs["reconcile:"+tenantKey] = reconcileSub
	r.mu.Unlock()

	report := r.readinessReport(ctx)
	if !report.Ready || report.Status != "healthy" || report.ActiveWorkers != 0 {
		t.Fatalf("pre-onboarding readiness report = %+v; want healthy ready state with 0 active workers", report)
	}
	if err := r.checkReadiness(ctx); err != nil {
		t.Fatalf("checkReadiness() = %v; want nil for pre-onboarding tenant", err)
	}

	// Alongside an isolated broken tenant, the daemon stays ready in degraded state.
	r.mu.Lock()
	r.tenantReadiness["tenant_dev_broken"] = tenantReconcileStatus{
		tenant:     tenant.Tenant{Name: "Broken", DBName: "tenant_dev_broken"},
		startupErr: errors.New("tenant schema preflight failed"),
	}
	r.mu.Unlock()
	degradedReport := r.readinessReport(ctx)
	if !degradedReport.Ready || degradedReport.Status != "degraded" {
		t.Fatalf("pre-onboarding + degraded report = %+v; want ready degraded state", degradedReport)
	}

	// Once a branch is expected, missing active workers must fail readiness.
	tc.expectedBranchesMu.Lock()
	tc.expectedBranches[7] = struct{}{}
	tc.expectedBranchesMu.Unlock()
	if err := r.checkReadiness(ctx); err == nil || !strings.Contains(err.Error(), "daemon has no active tenant workers") {
		t.Fatalf("readiness with expected branch and 0 workers = %v; want active worker error", err)
	}

	// Clearing expected branches restores readiness, but losing the onboarding responder fails readiness.
	tc.expectedBranchesMu.Lock()
	tc.expectedBranches = make(map[int64]struct{})
	tc.expectedBranchesMu.Unlock()
	if err := onboardSub.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	if err := r.checkReadiness(ctx); err == nil || !strings.Contains(err.Error(), "onboarding responder for tenant "+dbName) {
		t.Fatalf("readiness without onboarding responder = %v; want missing responder error", err)
	}
}

func TestDaemonReadinessFailsWhenAllTenantsFailedStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	r := newDaemonReconciler(ctx, nil, "", "", nil, nil, nil, &wg, time.Second, 1)
	r.mu.Lock()
	r.lastReconcileAt = time.Now()
	r.tenantReadiness["tenant_broken"] = tenantReconcileStatus{
		tenant:     tenant.Tenant{Name: "Broken", DBName: "tenant_broken"},
		startupErr: errors.New("tenant schema preflight failed"),
	}
	r.mu.Unlock()

	err := r.checkReadiness(ctx)
	if err == nil || !strings.Contains(err.Error(), "daemon has no active tenants") {
		t.Fatalf("readiness with only failed tenants = %v; want no active tenants error", err)
	}
}
