package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/processor"
	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/secutil"
	"github.com/zatca-go/zatca/storeconfig"
	"github.com/zatca-go/zatca/tenant"
	"github.com/zatca-go/zatca/zatca"
)

type branchEntry struct {
	svc *zatca.Service
	cfg *config.Config
}

type workerHandle struct {
	cancel context.CancelFunc
	done   <-chan struct{}
}

// daemonReconciler owns the live tenant and branch topology. The master
// registry is the only discovery source; NATS messages never create tenants.
type daemonReconciler struct {
	ctx        context.Context
	cancel     context.CancelFunc
	masterDB   *sql.DB
	baseDSN    string
	adminDSN   string
	encKey     *secutil.Key
	nc         *nats.Conn
	js         nats.JetStreamContext
	wg         *sync.WaitGroup
	ackWait    time.Duration
	maxDeliver int

	reconcileMu sync.Mutex
	mu          sync.RWMutex
	tenants     map[string]*tenantCtx
	workers     map[string]workerHandle
	subs        map[string]*nats.Subscription
	caches      map[string]map[int64]*branchEntry
	registry    *tenant.Registry

	lastReconcileAt  time.Time
	lastReconcileErr error
}

func newDaemonReconciler(
	ctx context.Context,
	masterDB *sql.DB,
	baseDSN, adminDSN string,
	encKey *secutil.Key,
	nc *nats.Conn,
	js nats.JetStreamContext,
	wg *sync.WaitGroup,
	ackWait time.Duration,
	maxDeliver int,
) *daemonReconciler {
	childCtx, cancel := context.WithCancel(ctx)
	return &daemonReconciler{
		ctx:        childCtx,
		cancel:     cancel,
		masterDB:   masterDB,
		baseDSN:    baseDSN,
		adminDSN:   adminDSN,
		encKey:     encKey,
		nc:         nc,
		js:         js,
		wg:         wg,
		ackWait:    ackWait,
		maxDeliver: maxDeliver,
		tenants:    make(map[string]*tenantCtx),
		workers:    make(map[string]workerHandle),
		subs:       make(map[string]*nats.Subscription),
		caches:     make(map[string]map[int64]*branchEntry),
		registry:   tenant.NewRegistry(),
	}
}

func (r *daemonReconciler) getService(dbName string, branchID int64) (*zatca.Service, *config.Config, error) {
	r.mu.RLock()
	tc, ok := r.tenants[dbName]
	if ok {
		if cache, exists := r.caches[dbName]; exists {
			if entry, exists := cache[branchID]; exists {
				r.mu.RUnlock()
				return entry.svc, entry.cfg, nil
			}
		}
	}
	r.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("unknown tenant db: %s", dbName)
	}

	bz, err := storeconfig.LoadBranchZATCA(tc.db, branchID, r.encKey)
	if err != nil {
		return nil, nil, fmt.Errorf("load branch %d ZATCA config: %w", branchID, err)
	}
	if !bz.IsRegistered() {
		return nil, nil, fmt.Errorf("branch %d not registered with ZATCA", branchID)
	}
	branchCfg := bz.ToConfig(tc.cfg)
	svc := bz.NewService(tc.cfg)

	r.mu.Lock()
	if r.caches[dbName] == nil {
		r.caches[dbName] = make(map[int64]*branchEntry)
	}
	r.caches[dbName][branchID] = &branchEntry{svc: svc, cfg: branchCfg}
	r.mu.Unlock()
	return svc, branchCfg, nil
}

func (r *daemonReconciler) reconcile(ctx context.Context) (reconcileErr error) {
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()
	defer func() {
		r.mu.Lock()
		r.lastReconcileAt = time.Now()
		r.lastReconcileErr = reconcileErr
		r.mu.Unlock()
	}()

	discovered, err := tenant.LoadAllContext(ctx, r.masterDB)
	if err != nil {
		return err
	}
	if _, err := r.registry.Reconcile(discovered); err != nil {
		return err
	}

	desired := make(map[string]tenant.Tenant, len(discovered))
	for _, current := range discovered {
		desired[current.DBName] = current
	}

	r.mu.RLock()
	current := make(map[string]*tenantCtx, len(r.tenants))
	for dbName, tc := range r.tenants {
		current[dbName] = tc
	}
	r.mu.RUnlock()

	for dbName, tc := range current {
		target, exists := desired[dbName]
		if !exists || target.TenantKey != tc.tenant.TenantKey {
			r.removeTenant(dbName)
			continue
		}
		if tc.tenant != target {
			r.removeTenant(dbName)
		}
	}

	var failures []error
	for _, target := range discovered {
		r.mu.RLock()
		_, exists := r.tenants[target.DBName]
		r.mu.RUnlock()
		if !exists {
			if err := r.addTenant(target); err != nil {
				failures = append(failures, fmt.Errorf("tenant %s (%s) failed to start: %w", target.Name, target.DBName, err))
			}
		}
	}

	r.mu.RLock()
	tenants := make([]*tenantCtx, 0, len(r.tenants))
	for _, tc := range r.tenants {
		tenants = append(tenants, tc)
	}
	r.mu.RUnlock()
	for _, tc := range tenants {
		if err := r.reconcileBranches(ctx, tc); err != nil {
			failures = append(failures, fmt.Errorf("tenant %s (%s) branch reconciliation failed: %w",
				tc.tenant.Name, tc.tenant.DBName, err))
		}
	}
	return errors.Join(failures...)
}

func (r *daemonReconciler) addTenant(t tenant.Tenant) error {
	if t.ZATCAEnv != "" {
		if _, err := config.ParseEnv(t.ZATCAEnv); err != nil {
			return fmt.Errorf("invalid ZATCA environment: %w", err)
		}
	}
	tdb, err := tenant.OpenDB(t, r.baseDSN)
	if err != nil {
		return err
	}
	chain := queue.NewSQLChain(tdb)
	if err := chain.Preflight(r.ctx); err != nil {
		tdb.Close()
		return err
	}
	env := config.Sandbox
	if t.ZATCAEnv != "" {
		env, _ = config.ParseEnv(t.ZATCAEnv)
	}
	tc := &tenantCtx{
		tenant:           t,
		db:               tdb,
		cfg:              &config.Config{Env: env},
		encKey:           r.encKey,
		claims:           queue.NewSQLClaimStore(tdb),
		chain:            chain,
		ackWait:          r.ackWait,
		maxDeliver:       r.maxDeliver,
		startBranch:      r.startBranch,
		expectedBranches: make(map[int64]struct{}),
	}
	if r.adminDSN != "" {
		if adminDB, adminErr := tenant.OpenDB(t, r.adminDSN); adminErr != nil {
			log.Printf("WARN: admin DB for tenant %s failed; onboarding uses runtime user: %v", t.Name, adminErr)
		} else {
			tc.adminDB = adminDB
		}
	}

	r.mu.Lock()
	r.tenants[t.DBName] = tc
	r.caches[t.DBName] = make(map[int64]*branchEntry)
	r.mu.Unlock()

	onboardSub, err := subscribeOnboardResponder(r.nc, r.js, tc, r.getService, r.wg)
	if err != nil {
		r.removeTenant(t.DBName)
		return fmt.Errorf("subscribe onboarding for %s: %w", t.DBName, err)
	}
	reconcileSub, err := subscribeReconcileResponder(r.nc, tc, r.handleReconcileRequest)
	if err != nil {
		_ = onboardSub.Unsubscribe()
		r.removeTenant(t.DBName)
		return fmt.Errorf("subscribe reconciliation for %s: %w", t.DBName, err)
	}
	r.mu.Lock()
	r.subs["onboard:"+t.DBName] = onboardSub
	r.subs["reconcile:"+t.TenantKey] = reconcileSub
	r.mu.Unlock()
	log.Printf("Tenant %s (%s) ready", t.Name, t.DBName)
	return nil
}

func (r *daemonReconciler) removeTenant(dbName string) {
	r.mu.Lock()
	tc := r.tenants[dbName]
	if tc != nil {
		tc.onboardingStateMu.Lock()
		tc.onboardingStopping = true
		tc.onboardingStateMu.Unlock()
	}
	delete(r.tenants, dbName)
	delete(r.caches, dbName)
	var workers []workerHandle
	prefix := dbName + ":"
	for key, worker := range r.workers {
		if strings.HasPrefix(key, prefix) {
			workers = append(workers, worker)
			delete(r.workers, key)
			unmarkBranchActive(dbName, branchIDFromKey(key))
		}
	}
	var subscriptions []*nats.Subscription
	if tc != nil {
		for key, sub := range r.subs {
			if key == "onboard:"+dbName || key == "reconcile:"+tc.tenant.TenantKey {
				subscriptions = append(subscriptions, sub)
				delete(r.subs, key)
			}
		}
	}
	r.mu.Unlock()
	for _, worker := range workers {
		worker.cancel()
	}
	for _, sub := range subscriptions {
		_ = sub.Unsubscribe()
	}
	if tc != nil {
		tc.onboardingWG.Wait()
	}
	for _, worker := range workers {
		<-worker.done
	}
	if tc != nil {
		_ = tc.db.Close()
		if tc.adminDB != nil {
			_ = tc.adminDB.Close()
		}
		log.Printf("Tenant %s removed from reconciliation", dbName)
	}
}

func (r *daemonReconciler) reconcileBranches(ctx context.Context, tc *tenantCtx) error {
	branches, err := processor.QueryActiveBranches(tc.db)
	if err != nil {
		return fmt.Errorf("query active branches: %w", err)
	}
	active := make(map[int64]bool, len(branches))
	for _, branchID := range branches {
		active[branchID] = true
		r.startBranch(tc, tc.tenant.DBName, branchID)
	}
	tc.expectedBranchesMu.Lock()
	tc.expectedBranches = make(map[int64]struct{}, len(branches))
	for _, branchID := range branches {
		tc.expectedBranches[branchID] = struct{}{}
	}
	tc.expectedBranchesMu.Unlock()
	var stopped []workerHandle
	r.mu.Lock()
	delete(r.caches, tc.tenant.DBName)
	for key, worker := range r.workers {
		if !strings.HasPrefix(key, tc.tenant.DBName+":") {
			continue
		}
		branchID := branchIDFromKey(key)
		if !active[branchID] {
			stopped = append(stopped, worker)
			delete(r.workers, key)
			unmarkBranchActive(tc.tenant.DBName, branchID)
		}
	}
	r.mu.Unlock()
	for _, worker := range stopped {
		worker.cancel()
	}
	for _, worker := range stopped {
		<-worker.done
	}
	if len(branches) == 0 {
		log.Printf("NOTE: no active branches in %s", tc.tenant.Name)
	}
	_ = ctx
	return nil
}

func (r *daemonReconciler) startBranch(tc *tenantCtx, dbName string, branchID int64) {
	key := branchKey(dbName, branchID)
	r.mu.Lock()
	if current, exists := r.tenants[dbName]; !exists || current != tc {
		r.mu.Unlock()
		return
	}
	if _, exists := r.workers[key]; exists {
		r.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	r.workers[key] = workerHandle{cancel: cancel, done: done}
	r.mu.Unlock()
	activeMu.Lock()
	activeBranches[key] = true
	activeMu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(done)
		runWorkerContext(workerCtx, r.js, tc, r.getService, dbName, branchID)
	}()
}

func branchIDFromKey(key string) int64 {
	var branchID int64
	if _, err := fmt.Sscanf(key[strings.LastIndexByte(key, ':')+1:], "%d", &branchID); err != nil {
		return 0
	}
	return branchID
}

func (r *daemonReconciler) loop(interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			if err := r.reconcile(r.ctx); err != nil {
				log.Printf("WARN: tenant reconciliation failed: %v", err)
			}
		}
	}
}

func (r *daemonReconciler) checkReadiness(context.Context) error {
	r.mu.RLock()
	reconciledAt := r.lastReconcileAt
	reconcileErr := r.lastReconcileErr
	tenants := make([]*tenantCtx, 0, len(r.tenants))
	for _, tc := range r.tenants {
		tenants = append(tenants, tc)
	}
	workers := make(map[string]workerHandle, len(r.workers))
	for key, worker := range r.workers {
		workers[key] = worker
	}
	subs := make(map[string]*nats.Subscription, len(r.subs))
	for key, sub := range r.subs {
		subs[key] = sub
	}
	js := r.js
	r.mu.RUnlock()

	if reconciledAt.IsZero() {
		return errors.New("tenant reconciliation has not completed")
	}
	if reconcileErr != nil {
		return fmt.Errorf("tenant reconciliation is unhealthy: %w", reconcileErr)
	}

	var missing []string
	for _, tc := range tenants {
		if sub := subs["onboard:"+tc.tenant.DBName]; sub == nil || !sub.IsValid() {
			missing = append(missing, fmt.Sprintf("onboarding responder for tenant %s", tc.tenant.DBName))
		}
		if sub := subs["reconcile:"+tc.tenant.TenantKey]; sub == nil || !sub.IsValid() {
			missing = append(missing, fmt.Sprintf("reconciliation responder for tenant %s", tc.tenant.TenantKey))
		}
		for _, branchID := range tc.expectedBranchSnapshot() {
			key := branchKey(tc.tenant.DBName, branchID)
			worker, ok := workers[key]
			if !ok || workerStopped(worker.done) {
				missing = append(missing, fmt.Sprintf("worker for %s branch %d", tc.tenant.DBName, branchID))
				continue
			}
			if js == nil {
				missing = append(missing, fmt.Sprintf("JetStream consumer for %s branch %d", tc.tenant.DBName, branchID))
				continue
			}
			if _, err := js.ConsumerInfo(queue.StreamName, queue.OrderedConsumerName(tc.tenant.DBName, branchID)); err != nil {
				missing = append(missing, fmt.Sprintf("JetStream consumer for %s branch %d: %v",
					tc.tenant.DBName, branchID, err))
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("daemon readiness is unhealthy; missing expected consumers: %s", strings.Join(missing, "; "))
	}
	return nil
}

func workerStopped(done <-chan struct{}) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (r *daemonReconciler) stop() {
	r.cancel()
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()

	r.mu.RLock()
	dbNames := make([]string, 0, len(r.tenants))
	for dbName := range r.tenants {
		dbNames = append(dbNames, dbName)
	}
	r.mu.RUnlock()
	for _, dbName := range dbNames {
		r.removeTenant(dbName)
	}
	r.wg.Wait()
}

func (r *daemonReconciler) handleReconcileRequest(ctx context.Context, tc *tenantCtx, req queue.ReconcileRequest) queue.ReconcileReply {
	if req.TenantKey != tc.tenant.TenantKey || req.DBName != tc.tenant.DBName {
		return queue.ReconcileReply{Error: "the request is for another tenant"}
	}
	stored, err := tc.chain.Stored(ctx, queue.Message{ID: req.BillID})
	if err != nil {
		return queue.ReconcileReply{Error: err.Error()}
	}
	if stored == nil {
		return queue.ReconcileReply{Error: fmt.Sprintf("bill %d has no committed invoice", req.BillID)}
	}
	if req.InvoiceUUID != "" && req.InvoiceUUID != stored.UUID {
		return queue.ReconcileReply{Error: "invoice UUID does not match the stored invoice"}
	}
	if req.InvoiceHash != "" && req.InvoiceHash != stored.Hash {
		return queue.ReconcileReply{Error: "invoice hash does not match the stored invoice"}
	}
	if req.Action == queue.ReconcileActionStatus {
		state := stored.Outcome
		if state == "" {
			state = "pending"
		}
		evidence := "recorded"
		if stored.OutcomeEvidence == queue.EvidenceExternal {
			evidence = "external"
		}
		return queue.ReconcileReply{
			Accepted:    true,
			State:       state,
			Evidence:    evidence,
			EvidenceID:  stored.EvidenceID,
			InvoiceHash: stored.Hash,
		}
	}
	if stored.Outcome != "" && stored.Outcome != queue.OutcomeAmbiguous {
		return queue.ReconcileReply{Error: "the invoice already has a final recorded outcome"}
	}
	if err := tc.chain.Record(ctx, *stored, queue.Outcome{
		State:        req.State,
		ProviderCode: req.ProviderCode,
		Message:      req.Message,
		Evidence:     queue.EvidenceExternal,
		EvidenceID:   req.EvidenceID,
	}); err != nil {
		return queue.ReconcileReply{Error: err.Error()}
	}
	return queue.ReconcileReply{
		Accepted:    true,
		State:       req.State,
		Evidence:    "external",
		EvidenceID:  req.EvidenceID,
		InvoiceHash: stored.Hash,
	}
}

func subscribeReconcileResponder(
	nc *nats.Conn,
	tc *tenantCtx,
	handle func(context.Context, *tenantCtx, queue.ReconcileRequest) queue.ReconcileReply,
) (*nats.Subscription, error) {
	return nc.QueueSubscribe(queue.ReconcileSubject(tc.tenant.TenantKey), "zatca-reconcilers", func(m *nats.Msg) {
		req, err := queue.DecodeReconcileRequest(m.Data)
		if err == nil && req.TenantKey != tc.tenant.TenantKey {
			err = errors.New("the request subject and tenant key do not match")
		}
		var reply queue.ReconcileReply
		if err != nil {
			reply.Error = err.Error()
		} else {
			reply = handle(context.Background(), tc, req)
		}
		data, marshalErr := json.Marshal(reply)
		if marshalErr != nil {
			log.Printf("ERROR: encode reconciliation reply: %v", marshalErr)
			return
		}
		if err := m.Respond(data); err != nil {
			log.Printf("ERROR: reply to reconciliation request: %v", err)
		}
	})
}
