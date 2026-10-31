package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
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

const maxRetries = 10

type tenantCtx struct {
	tenant  tenant.Tenant
	db      *sql.DB // source reads/result updates + backend chain contract writes
	adminDB *sql.DB // admin: also has INSERT/UPDATE on branch_zatca_config
	cfg     *config.Config
	encKey  *secutil.Key
	claims  queue.ClaimStore
	chain   queue.EGSSequenceStore
}

// activeBranches tracks which branches already have doc workers running.
var (
	activeMu       sync.Mutex
	activeBranches = map[string]bool{} // key: "dbName:branchID"
)

func branchKey(dbName string, branchID int64) string {
	return fmt.Sprintf("%s:%d", dbName, branchID)
}

func markBranchActive(dbName string, branchID int64) bool {
	activeMu.Lock()
	defer activeMu.Unlock()
	k := branchKey(dbName, branchID)
	if activeBranches[k] {
		return false // already running
	}
	activeBranches[k] = true
	return true
}

type getServiceFunc func(dbName string, branchID int64) (*zatca.Service, *config.Config, error)

// runWorker subscribes to one combined bill/credit/debit stream. The stream
// is partitioned by canonical tenant_id + egs_sequence_id by OrderedStream.
func runWorker(
	js nats.JetStreamContext,
	tenantMap map[string]*tenantCtx,
	getService getServiceFunc,
	dbName string,
	branchID int64,
) {
	subject := queue.OrderedSubject(dbName, branchID)
	consName := queue.OrderedConsumerName(dbName, branchID)
	pullAndProcessOrdered(js, tenantMap, getService, subject, consName, dbName, branchID)
}

// runOnboardWorker listens on zatca.onboard.{dbName}.* for branch registration requests.
// It drops expired OTP messages (>1 hour old) and deduplicates so that only the latest
// OTP per branch is processed when multiple are queued.
func runOnboardWorker(
	js nats.JetStreamContext,
	tenantMap map[string]*tenantCtx,
	getService getServiceFunc,
	wg *sync.WaitGroup,
	dbName string,
) {
	subject := fmt.Sprintf("zatca.onboard.%s.*", dbName)
	consName := fmt.Sprintf("zatca_onboard_%s", dbName)

	sub, err := js.PullSubscribe(subject, consName, nats.AckWait(5*time.Minute))
	if err != nil {
		log.Printf("ERROR: subscribe %s: %v", subject, err)
		return
	}
	log.Printf("Worker started: %s", subject)

	for {
		msgs, err := sub.Fetch(1, nats.MaxWait(30*time.Second))
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			log.Printf("ERROR: fetch %s: %v", subject, err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, raw := range msgs {
			var qmsg queue.Message
			if err := json.Unmarshal(raw.Data, &qmsg); err != nil {
				log.Printf("ERROR: unmarshal %s: %v", subject, err)
				raw.Ack()
				continue
			}

			msgAge := msgAgeDuration(raw)
			log.Printf("Onboard message received: branch=%d db=%s otp=%s age=%s",
				qmsg.BranchID, qmsg.DBName, maskOTP(qmsg.OTP), msgAge)

			// Drop if OTP expired (> 1 hour old)
			if isExpired(raw) {
				log.Printf("Dropping expired onboard: branch=%d db=%s otp=%s age=%s (>1h)",
					qmsg.BranchID, qmsg.DBName, maskOTP(qmsg.OTP), msgAge)
				raw.Ack()
				continue
			}

			// Drain pending messages and keep only the latest OTP per branch.
			// This ensures that if a user submitted multiple OTPs, only the
			// newest one is actually processed.
			latest := map[int64]*nats.Msg{qmsg.BranchID: raw}
			latestQ := map[int64]queue.Message{qmsg.BranchID: qmsg}

			for {
				batch, ferr := sub.Fetch(1, nats.MaxWait(200*time.Millisecond))
				if ferr != nil || len(batch) == 0 {
					break
				}
				var q queue.Message
				if json.Unmarshal(batch[0].Data, &q) != nil {
					batch[0].Ack()
					continue
				}
				bAge := msgAgeDuration(batch[0])
				log.Printf("Onboard queued msg: branch=%d db=%s otp=%s age=%s",
					q.BranchID, q.DBName, maskOTP(q.OTP), bAge)
				if isExpired(batch[0]) {
					log.Printf("Dropping expired onboard: branch=%d db=%s otp=%s age=%s (>1h)",
						q.BranchID, q.DBName, maskOTP(q.OTP), bAge)
					batch[0].Ack()
					continue
				}
				if prev, ok := latest[q.BranchID]; ok {
					oldQ := latestQ[q.BranchID]
					log.Printf("Newer OTP for branch %d in %s — dropping otp=%s, keeping otp=%s",
						q.BranchID, q.DBName, maskOTP(oldQ.OTP), maskOTP(q.OTP))
					prev.Ack()
				}
				latest[q.BranchID] = batch[0]
				latestQ[q.BranchID] = q
			}

			// Process the latest message for each branch
			for bid, m := range latest {
				q := latestQ[bid]
				log.Printf("Processing onboard: branch=%d db=%s otp=%s (latest OTP)",
					q.BranchID, q.DBName, maskOTP(q.OTP))

				// Onboard runs once — OTP is single-use, retrying would always fail with Invalid-OTP
				if err := processMessage(tenantMap, nil, q); err != nil {
					log.Printf("ERROR: onboard branch=%d FAILED: %v", q.BranchID, err)
					tc, ok := tenantMap[q.DBName]
					if ok && tc.tenant.AlertEmail != "" {
						sendAlert(tc.tenant.AlertEmail, q, err)
					}
					m.Ack()
					continue
				}
				spawnBranchWorkers(js, tenantMap, getService, wg, q.DBName, q.BranchID)
				m.Ack()
			}
		}
	}
}

// maskOTP shows the first 2 and last 2 chars of an OTP, masking the rest.
func maskOTP(otp string) string {
	if len(otp) <= 4 {
		return otp
	}
	return otp[:2] + "****" + otp[len(otp)-2:]
}

// msgAgeDuration returns a human-readable age of a NATS message.
func msgAgeDuration(msg *nats.Msg) string {
	meta, err := msg.Metadata()
	if err != nil {
		return "unknown"
	}
	return time.Since(meta.Timestamp).Truncate(time.Second).String()
}

// isExpired returns true if a NATS message is older than 1 hour (OTP lifetime).
func isExpired(msg *nats.Msg) bool {
	meta, err := msg.Metadata()
	if err != nil {
		return false // can't tell, assume valid
	}
	return time.Since(meta.Timestamp) > time.Hour
}

func pullAndProcessOrdered(
	js nats.JetStreamContext,
	tenantMap map[string]*tenantCtx,
	getService getServiceFunc,
	subject string,
	consName string,
	dbName string,
	branchID int64,
) {
	sub, err := js.PullSubscribe(subject, consName, nats.AckWait(5*time.Minute))
	if err != nil {
		log.Printf("ERROR: subscribe %s: %v", subject, err)
		return
	}
	tc, ok := tenantMap[dbName]
	if !ok {
		log.Printf("ERROR: ordered stream tenant %s is not loaded", dbName)
		return
	}
	ordered := queue.NewOrderedStream(
		tc.claims,
		tc.chain,
		func(ctx context.Context, msg queue.Message, reservation queue.EGSSequenceReservation) (queue.EGSSequenceCommitRequest, error) {
			return processOrderedMessage(tenantMap, getService, ctx, msg, reservation)
		},
	)
	log.Printf("Ordered worker started: %s", subject)
	// Fetching remains one-at-a-time so JetStream delivery order is preserved;
	// OrderedStream still owns the canonical partition lock for callers that
	// process deliveries concurrently.
	slots := make(chan struct{}, 1)

	for {
		msgs, err := sub.Fetch(1, nats.MaxWait(30*time.Second))
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			log.Printf("ERROR: fetch %s: %v", subject, err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, raw := range msgs {
			slots <- struct{}{}
			go func(raw *nats.Msg) {
				defer func() { <-slots }()

				qmsg, err := queue.DecodeCanonicalEnvelope(raw.Data)
				if err != nil {
					log.Printf("ERROR: decode ordered envelope on %s: %v", subject, err)
					return
				}
				if qmsg.DBName != dbName || qmsg.BranchID != branchID {
					log.Printf("ERROR: ordered envelope scope mismatch: got %s/%d, expected %s/%d",
						qmsg.DBName, qmsg.BranchID, dbName, branchID)
					return
				}
				if qmsg.TenantID != uint64(tc.tenant.ID) {
					log.Printf("ERROR: ordered envelope tenant mismatch: envelope=%d registry=%d",
						qmsg.TenantID, tc.tenant.ID)
					return
				}
				qmsg, err = enrichOrderedIdentity(getService, qmsg)
				if err != nil {
					log.Printf("ERROR: ordered envelope identity: %v", err)
					return
				}

				err = ordered.HandleMessage(
					context.Background(),
					qmsg,
					func() error { return raw.Ack() },
				)
				if err == nil {
					return
				}
				if errors.Is(err, queue.ErrFingerprintConflict) {
					log.Printf("ERROR: ordered message id=%d rejected: %v", qmsg.ID, err)
					return
				}
				if errors.Is(err, queue.ErrClaimInFlight) ||
					errors.Is(err, queue.ErrEGSSequenceBusy) {
					log.Printf("Ordered message still in flight: %s id=%d: %v", qmsg.DocType, qmsg.ID, err)
					return
				}

				log.Printf("ERROR: ordered message %s id=%d FAILED without ACK: %v", qmsg.DocType, qmsg.ID, err)
				if tc.tenant.AlertEmail != "" {
					sendAlert(tc.tenant.AlertEmail, qmsg, err)
				}
			}(raw)
		}
	}
}

func enrichOrderedIdentity(getService getServiceFunc, msg queue.Message) (queue.Message, error) {
	_, branchCfg, err := getService(msg.DBName, msg.BranchID)
	if err != nil {
		return queue.Message{}, err
	}
	if msg.VATNumber == "" {
		msg.VATNumber = branchCfg.Seller.VAT
	}
	if msg.EGSID == "" {
		msg.EGSID = branchCfg.CSR.SerialNumber
		if msg.EGSID == "" {
			// Older branch rows do not carry the CSR computer number. The
			// branch-scoped fallback remains stable, while the ordered
			// partition itself is still tenant_id + egs_sequence_id.
			msg.EGSID = fmt.Sprintf("branch-%d", msg.BranchID)
		}
	}
	if msg.VATNumber == "" {
		return queue.Message{}, errors.New("ordered envelope is missing seller VAT number")
	}
	return msg, nil
}

func processOrderedMessage(
	tenantMap map[string]*tenantCtx,
	getService getServiceFunc,
	_ context.Context,
	msg queue.Message,
	reservation queue.EGSSequenceReservation,
) (queue.EGSSequenceCommitRequest, error) {
	state := processor.DocumentChainState{
		DocumentUUID:        reservation.DocumentUUID,
		ICV:                 reservation.ICV,
		PreviousInvoiceHash: reservation.PIH,
	}
	hash, err := processMessageWithChain(tenantMap, getService, msg, state)
	if err != nil {
		return queue.EGSSequenceCommitRequest{}, err
	}
	snapshot, err := json.Marshal(struct {
		SourceID      uint64  `json:"source_id"`
		DocumentType  string  `json:"document_type"`
		TenantID      uint64  `json:"tenant_id"`
		EGSSequenceID string  `json:"egs_sequence_id"`
		DocumentUUID  string  `json:"document_uuid"`
		ICV           uint64  `json:"icv"`
		PIH           *string `json:"pih"`
		DocumentHash  string  `json:"document_hash"`
		OutboxEventID string  `json:"outbox_event_id"`
		PosID         string  `json:"pos_id"`
		BillRequestID string  `json:"bill_request_id"`
		RequestHash   string  `json:"request_fingerprint,omitempty"`
	}{
		SourceID:      uint64(msg.ID),
		DocumentType:  string(msg.DocType),
		TenantID:      msg.TenantID,
		EGSSequenceID: msg.EGSSequenceID,
		DocumentUUID:  reservation.DocumentUUID,
		ICV:           reservation.ICV,
		PIH:           reservation.PIH,
		DocumentHash:  hash,
		OutboxEventID: reservation.OutboxEventID,
		PosID:         msg.PosID,
		BillRequestID: msg.BillRequestID,
		RequestHash:   msg.BusinessFingerprint(),
	})
	if err != nil {
		return queue.EGSSequenceCommitRequest{}, fmt.Errorf("marshal immutable EGS snapshot: %w", err)
	}
	return queue.EGSSequenceCommitRequest{
		Reservation:  reservation,
		DocumentType: string(msg.DocType),
		SourceID:     uint64(msg.ID),
		State:        queue.EGSDocumentQueued,
		Snapshot:     snapshot,
		DocumentHash: hash,
	}, nil
}

func processMessageWithChain(
	tenantMap map[string]*tenantCtx,
	getService getServiceFunc,
	msg queue.Message,
	chainState processor.DocumentChainState,
) (string, error) {
	tc, ok := tenantMap[msg.DBName]
	if !ok {
		return "", fmt.Errorf("unknown tenant db: %s (known tenants: %v)", msg.DBName, tenantKeys(tenantMap))
	}

	svc, branchCfg, err := getService(msg.DBName, msg.BranchID)
	if err != nil {
		return "", fmt.Errorf("get service for branch %d: %w", msg.BranchID, err)
	}

	switch msg.DocType {
	case queue.DocBill:
		bill, err := processor.QueryBillByID(tc.db, msg.ID)
		if err != nil {
			return "", err
		}
		return processor.ProcessBillWithChain(tc.db, svc, branchCfg, *bill, chainState)
	case queue.DocCredit:
		row, err := processor.QueryCreditNoteByID(tc.db, msg.ID)
		if err != nil {
			return "", err
		}
		return processor.ProcessCreditNoteWithChain(tc.db, svc, branchCfg, *row, chainState)
	case queue.DocDebit:
		row, err := processor.QueryDebitNoteByID(tc.db, msg.ID)
		if err != nil {
			return "", err
		}
		return processor.ProcessDebitNoteWithChain(tc.db, svc, branchCfg, *row, chainState)
	default:
		return "", fmt.Errorf("unknown ordered doc type: %s", msg.DocType)
	}
}

// processWithRetry is retained for compatibility with standalone callers.
func processWithRetry(
	tenantMap map[string]*tenantCtx,
	getService getServiceFunc,
	msg queue.Message,
) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
			log.Printf("  Retry %d/%d for %s id=%d in %v", attempt+1, maxRetries, msg.DocType, msg.ID, backoff)
			time.Sleep(backoff)
		}

		err := processMessage(tenantMap, getService, msg)
		if err == nil {
			return nil
		}
		lastErr = err
		log.Printf("  Attempt %d failed for %s id=%d: %v", attempt+1, msg.DocType, msg.ID, err)
	}
	return fmt.Errorf("all %d retries exhausted: %w", maxRetries, lastErr)
}

// processMessage handles a single queue message by querying the tenant DB and processing.
func processMessage(
	tenantMap map[string]*tenantCtx,
	getService getServiceFunc,
	msg queue.Message,
) error {
	tc, ok := tenantMap[msg.DBName]
	if !ok {
		return fmt.Errorf("unknown tenant db: %s (known tenants: %v)", msg.DBName, tenantKeys(tenantMap))
	}

	log.Printf("Processing %s id=%d branch=%d db=%s", msg.DocType, msg.ID, msg.BranchID, msg.DBName)

	// Onboard doesn't need an existing ZATCA service — it creates one
	// Uses adminDB which has INSERT/UPDATE on branch_zatca_config
	if msg.DocType == queue.DocOnboard {
		db := tc.db
		if tc.adminDB != nil {
			db = tc.adminDB
		}
		return processor.OnboardBranch(db, tc.cfg, msg.BranchID, msg.OTP, tc.encKey)
	}

	svc, branchCfg, err := getService(msg.DBName, msg.BranchID)
	if err != nil {
		return fmt.Errorf("get service for branch %d: %w", msg.BranchID, err)
	}

	switch msg.DocType {
	case queue.DocBill:
		bill, err := processor.QueryBillByID(tc.db, msg.ID)
		if err != nil {
			return err
		}
		_, err = processor.ProcessBill(tc.db, svc, branchCfg, *bill)
		return err

	case queue.DocCredit:
		row, err := processor.QueryCreditNoteByID(tc.db, msg.ID)
		if err != nil {
			return err
		}
		_, err = processor.ProcessCreditNote(tc.db, svc, branchCfg, *row)
		return err

	case queue.DocDebit:
		row, err := processor.QueryDebitNoteByID(tc.db, msg.ID)
		if err != nil {
			return err
		}
		_, err = processor.ProcessDebitNote(tc.db, svc, branchCfg, *row)
		return err

	default:
		return fmt.Errorf("unknown doc type: %s", msg.DocType)
	}
}

// spawnBranchWorkers starts one combined ordered worker for a branch if not
// already running.
func spawnBranchWorkers(
	js nats.JetStreamContext,
	tenantMap map[string]*tenantCtx,
	getService getServiceFunc,
	wg *sync.WaitGroup,
	dbName string,
	branchID int64,
) {
	if !markBranchActive(dbName, branchID) {
		return // workers already running for this branch
	}
	log.Printf("Spawning ordered worker for branch %d in %s (post-onboard)", branchID, dbName)
	wg.Add(1)
	go func() {
		defer wg.Done()
		runWorker(js, tenantMap, getService, dbName, branchID)
	}()
}

func tenantKeys(m map[string]*tenantCtx) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
