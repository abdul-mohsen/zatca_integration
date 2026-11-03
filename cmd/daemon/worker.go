package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/processor"
	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/secutil"
	"github.com/zatca-go/zatca/tenant"
	"github.com/zatca-go/zatca/zatca"
)

// tenantCtx is one tenant the daemon serves.
type tenantCtx struct {
	tenant  tenant.Tenant
	db      *sql.DB // source reads and chain writes
	adminDB *sql.DB // admin: onboarding writes to branch_zatca_config
	cfg     *config.Config
	encKey  *secutil.Key
	claims  queue.ClaimStore
	chain   queue.Chain
}

type getServiceFunc func(dbName string, branchID int64) (*zatca.Service, *config.Config, error)

// activeBranches tracks which branches already have a worker, and onboarding tracks which have an onboarding running.
var (
	activeMu       sync.Mutex
	activeBranches = map[string]bool{}
	onboardingMu   sync.Mutex
	onboarding     = map[string]bool{}
)

func branchKey(dbName string, branchID int64) string {
	return fmt.Sprintf("%s:%d", dbName, branchID)
}

func markBranchActive(dbName string, branchID int64) bool {
	activeMu.Lock()
	defer activeMu.Unlock()
	k := branchKey(dbName, branchID)
	if activeBranches[k] {
		return false
	}
	activeBranches[k] = true
	return true
}

func beginOnboarding(dbName string, branchID int64) bool {
	onboardingMu.Lock()
	defer onboardingMu.Unlock()
	k := branchKey(dbName, branchID)
	if onboarding[k] {
		return false
	}
	onboarding[k] = true
	return true
}

func endOnboarding(dbName string, branchID int64) {
	onboardingMu.Lock()
	defer onboardingMu.Unlock()
	delete(onboarding, branchKey(dbName, branchID))
}

// runWorker consumes one branch's documents in order. The ordered stream partitions them by tenant and EGS.
func runWorker(js nats.JetStreamContext, tc *tenantCtx, getService getServiceFunc, dbName string, branchID int64) {
	subject := queue.OrderedSubject(dbName, branchID)
	sub, err := js.PullSubscribe(subject, queue.OrderedConsumerName(dbName, branchID), nats.AckWait(5*time.Minute))
	if err != nil {
		log.Printf("ERROR: subscribe %s: %v", subject, err)
		return
	}
	stream := queue.NewOrderedStream(tc.claims, tc.chain, &processor.DocumentPipeline{
		DB: tc.db,
		Service: func(branch int64) (*zatca.Service, *config.Config, error) {
			return getService(dbName, branch)
		},
	})
	log.Printf("Ordered worker started: %s", subject)

	for {
		msgs, err := sub.Fetch(1, nats.MaxWait(30*time.Second))
		if err != nil {
			if !errors.Is(err, nats.ErrTimeout) {
				log.Printf("ERROR: fetch %s: %v", subject, err)
				time.Sleep(5 * time.Second)
			}
			continue
		}
		for _, raw := range msgs {
			handleDelivery(stream, tc, dbName, branchID, raw)
		}
	}
}

// handleDelivery runs one delivery. An envelope that can never be processed is terminated, so it is not redelivered. An
// envelope for another tenant or branch is terminated as well. Any other failure leaves the delivery for a redelivery.
func handleDelivery(stream *queue.OrderedStream, tc *tenantCtx, dbName string, branchID int64, raw *nats.Msg) {
	msg, err := queue.DecodeEnvelope(raw.Data)
	if err != nil {
		log.Printf("ERROR: terminating invalid envelope on %s: %v", raw.Subject, err)
		_ = raw.Term()
		return
	}
	if msg.DBName != dbName || msg.BranchID != branchID || msg.TenantKey != tc.tenant.TenantKey {
		log.Printf("ERROR: terminating envelope for %s/%d/%s on %s", msg.DBName, msg.BranchID, msg.TenantKey, raw.Subject)
		_ = raw.Term()
		return
	}

	err = stream.HandleMessage(context.Background(), msg, func() error { return raw.Ack() })
	switch {
	case err == nil:
	case errors.Is(err, queue.ErrClaimInFlight), errors.Is(err, queue.ErrChainBusy):
		log.Printf("Document still in flight: %s %d: %v", msg.DocType, msg.ID, err)
	case errors.Is(err, queue.ErrInvoiceRejected):
		log.Printf("ERROR: ZATCA rejected verified %s %d: %v", msg.DocType, msg.ID, err)
		alert(tc, fmt.Sprintf("%s %d of branch %d rejected by ZATCA", msg.DocType, msg.ID, msg.BranchID), err)
	case errors.Is(err, queue.ErrRejectedInput), errors.Is(err, queue.ErrFingerprintConflict):
		log.Printf("ERROR: %s %d not invoiced: %v", msg.DocType, msg.ID, err)
		alert(tc, fmt.Sprintf("%s %d of branch %d", msg.DocType, msg.ID, msg.BranchID), err)
	default:
		log.Printf("ERROR: %s %d FAILED without ACK, it is retried: %v", msg.DocType, msg.ID, err)
		alert(tc, fmt.Sprintf("%s %d of branch %d", msg.DocType, msg.ID, msg.BranchID), err)
	}
}

func alert(tc *tenantCtx, what string, err error) {
	if tc.tenant.AlertEmail != "" {
		sendAlert(tc.tenant.AlertEmail, what, tc.tenant.DBName, err)
	}
}

// runOnboardResponder answers the onboarding requests of one tenant. A request is checked and answered at once. Then one
// goroutine onboards the branch with the OTP, which is held in memory only, and writes the result to branch_zatca_config.
// The request is a core NATS message on a subject outside the stream, so no copy of the OTP is stored.
func runOnboardResponder(nc *nats.Conn, js nats.JetStreamContext, tc *tenantCtx, getService getServiceFunc, wg *sync.WaitGroup) error {
	dbName := tc.tenant.DBName
	_, err := nc.Subscribe(queue.OnboardWildcard(dbName), func(m *nats.Msg) {
		req, err := queue.DecodeOnboardRequest(m.Data)
		if err == nil {
			err = checkOnboardRequest(m.Subject, tc, req)
		}
		if err == nil && !beginOnboarding(dbName, req.BranchID) {
			err = errors.New("onboarding of this branch is already running")
		}
		replyOnboard(m, err)
		if err != nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer endOnboarding(dbName, req.BranchID)
			onboardBranch(js, tc, getService, wg, req)
		}()
	})
	return err
}

// checkOnboardRequest refuses a request for another tenant, or one whose subject does not name its branch.
func checkOnboardRequest(subject string, tc *tenantCtx, req queue.OnboardRequest) error {
	if req.DBName != tc.tenant.DBName || req.TenantKey != tc.tenant.TenantKey {
		return errors.New("the request is for another tenant")
	}
	if subject != queue.OnboardSubject(req.DBName, req.BranchID) {
		return errors.New("the request subject does not name its branch")
	}
	return nil
}

func replyOnboard(m *nats.Msg, cause error) {
	reply := queue.OnboardReply{Accepted: cause == nil}
	if cause != nil {
		reply.Error = cause.Error()
	}
	data, err := json.Marshal(reply)
	if err != nil {
		log.Printf("ERROR: encode onboarding reply: %v", err)
		return
	}
	if err := m.Respond(data); err != nil {
		log.Printf("ERROR: reply to onboarding on %s: %v", m.Subject, err)
	}
}

// onboardBranch runs the onboarding of one branch. On success the branch's worker starts. On failure the tenant is alerted.
func onboardBranch(js nats.JetStreamContext, tc *tenantCtx, getService getServiceFunc, wg *sync.WaitGroup, req queue.OnboardRequest) {
	log.Printf("Processing onboard: branch=%d db=%s", req.BranchID, req.DBName)
	db := tc.db
	if tc.adminDB != nil {
		db = tc.adminDB
	}
	if err := processor.OnboardBranch(db, tc.cfg, req.BranchID, req.OTP, tc.encKey); err != nil {
		log.Printf("ERROR: onboard branch=%d FAILED: %v", req.BranchID, err)
		alert(tc, fmt.Sprintf("onboarding of branch %d", req.BranchID), err)
		return
	}
	spawnBranchWorkers(js, tc, getService, wg, req.DBName, req.BranchID)
}

// spawnBranchWorkers starts the ordered worker of a branch, unless it is already running.
func spawnBranchWorkers(js nats.JetStreamContext, tc *tenantCtx, getService getServiceFunc, wg *sync.WaitGroup, dbName string, branchID int64) {
	if !markBranchActive(dbName, branchID) {
		return
	}
	log.Printf("Spawning ordered worker for branch %d in %s", branchID, dbName)
	wg.Add(1)
	go func() {
		defer wg.Done()
		runWorker(js, tc, getService, dbName, branchID)
	}()
}
