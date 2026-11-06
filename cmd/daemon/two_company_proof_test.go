package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/dbutil"
	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/tenant"
)

// This is intentionally a local stub proof. It does not claim that a real
// MySQL host, Docker/Dokku deployment, ZATCA SDK, or ZATCA provider is
// reachable. Those evidence boundaries require the external environment.
type proofBill struct {
	ID   int64
	Name string
}

type proofStore struct {
	bills map[int64]proofBill
}

func (s *proofStore) create(name string) proofBill {
	var id int64 = 1
	for {
		if _, exists := s.bills[id]; !exists {
			break
		}
		id++
	}
	bill := proofBill{ID: id, Name: name}
	s.bills[id] = bill
	return bill
}

func (s *proofStore) list() []proofBill {
	bills := make([]proofBill, 0, len(s.bills))
	for _, bill := range s.bills {
		bills = append(bills, bill)
	}
	sort.Slice(bills, func(i, j int) bool { return bills[i].ID < bills[j].ID })
	return bills
}

func (s *proofStore) detail(id int64) (proofBill, bool) {
	bill, ok := s.bills[id]
	return bill, ok
}

type proofCompany struct {
	tenant     tenant.Tenant
	dbUser     string
	dbPassword string
	daemonDir  string
	natsDir    string
	branchID   int64
	store      *proofStore
	claims     *proofClaims
	chain      *proofChain
	pipeline   *proofPipeline
}

func newProofCompany(root, name, key, dbName, dbUser string, registryID int64) *proofCompany {
	daemonDir := filepath.Join(root, name, "daemon")
	natsDir := filepath.Join(daemonDir, "nats")
	_ = os.MkdirAll(natsDir, 0o700)

	company := &proofCompany{
		tenant: tenant.Tenant{
			ID:        registryID,
			Name:      name,
			TenantKey: key,
			DBName:    dbName,
			ZATCAEnv:  "simulation",
		},
		dbUser:     dbUser,
		dbPassword: "proof-password-" + key,
		daemonDir:  daemonDir,
		natsDir:    natsDir,
		branchID:   7,
		store:      &proofStore{bills: map[int64]proofBill{}},
		claims:     newProofClaims(),
	}
	company.chain = &proofChain{
		store:    company.store,
		invoices: map[int64]queue.SignedInvoice{},
	}
	company.pipeline = &proofPipeline{company: company}
	return company
}

// listBills and detailBill model the local tenant API boundary: a request
// naming another tenant key is rejected before it reaches the tenant store.
func (c *proofCompany) listBills(requestTenantKey string) []proofBill {
	if requestTenantKey != c.tenant.TenantKey {
		return nil
	}
	return c.store.list()
}

func (c *proofCompany) detailBill(requestTenantKey string, billID int64) (proofBill, error) {
	if requestTenantKey != c.tenant.TenantKey {
		return proofBill{}, errors.New("foreign tenant")
	}
	bill, ok := c.store.detail(billID)
	if !ok {
		return proofBill{}, errors.New("bill not found")
	}
	return bill, nil
}

type proofClaims struct {
	states       map[string]string
	fingerprints map[string]string
	claims       int
	completes    int
	releases     int
}

func newProofClaims() *proofClaims {
	return &proofClaims{
		states:       map[string]string{},
		fingerprints: map[string]string{},
	}
}

func (c *proofClaims) Claim(_ context.Context, msg queue.Message) (queue.ClaimDecision, error) {
	c.claims++
	key := msg.RequestKey()
	if existing := c.fingerprints[key]; existing != "" && existing != msg.RequestFingerprint {
		return queue.ClaimConflict, nil
	}
	c.fingerprints[key] = msg.RequestFingerprint
	switch c.states[key] {
	case "completed":
		return queue.ClaimDuplicate, nil
	case "processing":
		return queue.ClaimInFlight, nil
	default:
		c.states[key] = "processing"
		return queue.ClaimProcess, nil
	}
}

func (c *proofClaims) Complete(_ context.Context, msg queue.Message) error {
	c.completes++
	c.states[msg.RequestKey()] = "completed"
	return nil
}

func (c *proofClaims) Release(_ context.Context, msg queue.Message) error {
	c.releases++
	delete(c.states, msg.RequestKey())
	return nil
}

type proofChain struct {
	store    *proofStore
	invoices map[int64]queue.SignedInvoice
	lastHash string
	lastICV  uint64
	stored   int
	books    int
	commits  int
	records  int
}

func (c *proofChain) Stored(_ context.Context, msg queue.Message) (*queue.SignedInvoice, error) {
	c.stored++
	invoice, ok := c.invoices[msg.ID]
	if !ok {
		return nil, nil
	}
	copy := invoice
	return &copy, nil
}

func (c *proofChain) Book(_ context.Context, msg queue.Message) (queue.Booking, error) {
	c.books++
	pih := queue.GenesisPIH
	if c.lastHash != "" {
		pih = c.lastHash
	}
	return queue.Booking{
		BranchID:   msg.BranchID,
		RequestKey: msg.RequestKey(),
		EventID:    msg.EventID,
		UUID:       msg.InvoiceUUID,
		ICV:        c.lastICV + 1,
		PIH:        pih,
	}, nil
}

func (c *proofChain) Release(context.Context, queue.Booking) error {
	return nil
}

func (c *proofChain) Commit(_ context.Context, booking queue.Booking, invoice queue.SignedInvoice) error {
	if _, ok := c.store.detail(invoice.SourceID); !ok {
		return fmt.Errorf("bill %d is not in this tenant database", invoice.SourceID)
	}
	if invoice.ICV != booking.ICV || invoice.PIH != booking.PIH || invoice.UUID != booking.UUID {
		return errors.New("invoice does not match the booking")
	}
	c.commits++
	c.invoices[invoice.SourceID] = invoice
	c.lastICV = invoice.ICV
	c.lastHash = invoice.Hash
	return nil
}

func (c *proofChain) Record(_ context.Context, invoice queue.SignedInvoice, outcome queue.Outcome) error {
	c.records++
	stored, ok := c.invoices[invoice.SourceID]
	if !ok {
		return errors.New("invoice was not committed")
	}
	stored.Outcome = outcome.State
	c.invoices[invoice.SourceID] = stored
	return nil
}

type proofPipeline struct {
	company  *proofCompany
	prepares int
	builds   int
	submits  int
}

func (p *proofPipeline) Prepare(_ context.Context, msg queue.Message) (queue.Prepared, error) {
	if msg.DBName != p.company.tenant.DBName || msg.TenantKey != p.company.tenant.TenantKey {
		return nil, fmt.Errorf("%w: foreign tenant", queue.ErrRejectedInput)
	}
	if _, ok := p.company.store.detail(msg.ID); !ok {
		return nil, fmt.Errorf("%w: bill %d does not exist", queue.ErrRejectedInput, msg.ID)
	}
	p.prepares++
	return proofPrepared{pipeline: p, msg: msg}, nil
}

func (p *proofPipeline) Submit(_ context.Context, invoice queue.SignedInvoice) (queue.Outcome, error) {
	p.submits++
	return queue.Outcome{State: queue.OutcomeAccepted}, nil
}

type proofPrepared struct {
	pipeline *proofPipeline
	msg      queue.Message
}

func (p proofPrepared) Build(_ context.Context, booking queue.Booking) (queue.SignedInvoice, error) {
	p.pipeline.builds++
	return queue.SignedInvoice{
		SourceID:  p.msg.ID,
		BranchID:  booking.BranchID,
		UUID:      booking.UUID,
		ICV:       booking.ICV,
		PIH:       booking.PIH,
		Hash:      fmt.Sprintf("hash-%s-%d", p.pipeline.company.tenant.TenantKey, p.msg.ID),
		Kind:      queue.InvoiceStandard,
		SignedXML: fmt.Sprintf("<invoice tenant=%q bill=%d/>", p.pipeline.company.tenant.TenantKey, p.msg.ID),
		QR:        "local-stub-qr",
	}, nil
}

type proofNATS struct {
	server *server.Server
	nc     *nats.Conn
	js     nats.JetStreamContext
}

func startProofNATS(t *testing.T, storeDir, name string) *proofNATS {
	t.Helper()
	_ = name
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
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
	if _, err := js.StreamInfo(queue.StreamName); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      queue.StreamName,
			Subjects:  []string{queue.SubjectAll},
			Retention: nats.WorkQueuePolicy,
			Storage:   nats.FileStorage,
			MaxAge:    time.Hour,
		})
		if err != nil {
			nc.Close()
			ns.Shutdown()
			t.Fatal(err)
		}
	}
	return &proofNATS{server: ns, nc: nc, js: js}
}

func (n *proofNATS) close() {
	if n == nil {
		return
	}
	n.nc.Close()
	n.server.Shutdown()
	n.server.WaitForShutdown()
}

func proofSubject(c *proofCompany) string {
	return queue.OrderedSubject(c.tenant.DBName, c.branchID)
}

func proofMessage(c *proofCompany, billID int64, requestID string) queue.Message {
	return queue.Message{
		DocType:            queue.DocBill,
		ID:                 billID,
		BranchID:           c.branchID,
		DBName:             c.tenant.DBName,
		TenantKey:          c.tenant.TenantKey,
		PosID:              c.tenant.TenantKey + "-pos",
		BillRequestID:      requestID,
		EventID:            uuid.NewString(),
		EGSSequenceID:      strconv.FormatInt(c.branchID, 10),
		InvoiceUUID:        uuid.NewString(),
		RequestFingerprint: strings.Repeat("a", 64),
	}
}

func publishProof(t *testing.T, n *proofNATS, subject string, msg queue.Message) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.js.Publish(subject, data, nats.MsgId(uuid.NewString())); err != nil {
		t.Fatal(err)
	}
}

func fetchProof(t *testing.T, sub *nats.Subscription) *nats.Msg {
	t.Helper()
	msgs, err := sub.Fetch(1, nats.MaxWait(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("fetched %d messages; want one", len(msgs))
	}
	return msgs[0]
}

func newProofStream(c *proofCompany) *queue.OrderedStream {
	return queue.NewOrderedStream(c.claims, c.chain, c.pipeline)
}

func TestTwoCompanyLocalStubProof(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	a := newProofCompany(root, "company-a", "company-a-key", "company_a_db", "company_a_user", 1)
	b := newProofCompany(root, "company-b", "company-b-key", "company_b_db", "company_b_user", 2)

	if err := tenant.ValidateAll([]tenant.Tenant{a.tenant, b.tenant}); err != nil {
		t.Fatalf("distinct tenant registry rows rejected: %v", err)
	}
	if a.tenant.TenantKey == b.tenant.TenantKey || a.tenant.DBName == b.tenant.DBName {
		t.Fatal("companies do not have distinct tenant keys and databases")
	}
	if a.dbUser == b.dbUser || a.dbPassword == b.dbPassword {
		t.Fatal("companies do not have distinct database credentials")
	}
	dsnA := dbutil.FormatDSN(a.dbUser, a.dbPassword, "shared-db-host", "3306", a.tenant.DBName)
	dsnB := dbutil.FormatDSN(b.dbUser, b.dbPassword, "shared-db-host", "3306", b.tenant.DBName)
	cfgA, err := mysql.ParseDSN(dsnA)
	if err != nil {
		t.Fatal(err)
	}
	cfgB, err := mysql.ParseDSN(dsnB)
	if err != nil {
		t.Fatal(err)
	}
	if cfgA.Addr != cfgB.Addr || cfgA.DBName == cfgB.DBName || cfgA.User == cfgB.User {
		t.Fatalf("connection proof does not show one host with separate routing: A=%+v B=%+v", cfgA, cfgB)
	}

	aBill := a.store.create("company-a-only")
	bBill := b.store.create("company-b-only")
	if got := a.listBills(a.tenant.TenantKey); len(got) != 1 || got[0].Name != aBill.Name {
		t.Fatalf("company A list leaked or lost data: %+v", got)
	}
	if got := b.listBills(b.tenant.TenantKey); len(got) != 1 || got[0].Name != bBill.Name {
		t.Fatalf("company B list leaked or lost data: %+v", got)
	}
	if got := a.listBills(b.tenant.TenantKey); len(got) != 0 {
		t.Fatalf("company A list accepted company B key: %+v", got)
	}
	if _, err := a.detailBill(b.tenant.TenantKey, bBill.ID); err == nil {
		t.Fatal("company A detail accepted company B key")
	}
	aDetail, err := a.detailBill(a.tenant.TenantKey, aBill.ID)
	if err != nil || aDetail.Name != aBill.Name {
		t.Fatalf("company A detail = %+v, %v", aDetail, err)
	}
	bDetail, err := b.detailBill(b.tenant.TenantKey, bBill.ID)
	if err != nil || bDetail.Name != bBill.Name {
		t.Fatalf("company B detail = %+v, %v", bDetail, err)
	}

	aNATS := startProofNATS(t, a.natsDir, "proof-company-a")
	bNATS := startProofNATS(t, b.natsDir, "proof-company-b")
	defer func() {
		if aNATS != nil {
			aNATS.close()
		}
		if bNATS != nil {
			bNATS.close()
		}
	}()
	if a.natsDir == b.natsDir || a.natsDir == "" || b.natsDir == "" {
		t.Fatal("companies do not have private NATS persistence paths")
	}
	if entries, err := os.ReadDir(a.natsDir); err != nil || len(entries) == 0 {
		t.Fatalf("company A NATS path has no local persistence evidence: entries=%d err=%v", len(entries), err)
	}
	if entries, err := os.ReadDir(b.natsDir); err != nil || len(entries) == 0 {
		t.Fatalf("company B NATS path has no local persistence evidence: entries=%d err=%v", len(entries), err)
	}

	subA, err := aNATS.js.PullSubscribe(proofSubject(a), queue.OrderedConsumerName(a.tenant.DBName, a.branchID))
	if err != nil {
		t.Fatal(err)
	}
	if subA == nil {
		t.Fatal("company A durable consumer was not created")
	}
	subB, err := bNATS.js.PullSubscribe(proofSubject(b), queue.OrderedConsumerName(b.tenant.DBName, b.branchID))
	if err != nil {
		t.Fatal(err)
	}
	msgA := proofMessage(a, aBill.ID, "company-a-request-1")
	msgB := proofMessage(b, bBill.ID, "company-b-request-1")
	publishProof(t, aNATS, proofSubject(a), msgA)
	publishProof(t, bNATS, proofSubject(b), msgB)

	// Process B while A remains pending. The two workers have separate claim
	// and chain stores even though both NATS servers use the same host.
	rawB := fetchProof(t, subB)
	handleDelivery(newProofStream(b), &tenantCtx{tenant: b.tenant}, b.tenant.DBName, b.branchID, rawB)
	if b.pipeline.prepares != 1 || b.pipeline.submits != 1 || b.chain.commits != 1 {
		t.Fatalf("company B processing = prepare %d submit %d commit %d; want one each",
			b.pipeline.prepares, b.pipeline.submits, b.chain.commits)
	}
	if a.pipeline.prepares != 0 || a.claims.claims != 0 || a.chain.stored != 0 {
		t.Fatalf("company A was touched by company B event: pipeline=%d claims=%d stored=%d",
			a.pipeline.prepares, a.claims.claims, a.chain.stored)
	}

	// A foreign envelope on B's subject is terminated before B's claim,
	// detail, or chain code runs.
	publishProof(t, bNATS, proofSubject(b), msgA)
	rawForeign := fetchProof(t, subB)
	handleDelivery(newProofStream(b), &tenantCtx{tenant: b.tenant}, b.tenant.DBName, b.branchID, rawForeign)
	if b.pipeline.prepares != 1 || b.pipeline.submits != 1 || b.claims.claims != 1 || b.chain.commits != 1 {
		t.Fatalf("foreign company A event crossed B boundary: pipeline=%d/%d claims=%d commits=%d",
			b.pipeline.prepares, b.pipeline.submits, b.claims.claims, b.chain.commits)
	}

	// Stop only A's private daemon/NATS path before its pending event is
	// acknowledged, then recover the same durable consumer from that path.
	aNATS.close()
	aNATS = startProofNATS(t, a.natsDir, "proof-company-a")
	subARecovered, err := aNATS.js.PullSubscribe(proofSubject(a), queue.OrderedConsumerName(a.tenant.DBName, a.branchID))
	if err != nil {
		t.Fatal(err)
	}
	rawA := fetchProof(t, subARecovered)
	handleDelivery(newProofStream(a), &tenantCtx{tenant: a.tenant}, a.tenant.DBName, a.branchID, rawA)
	if a.pipeline.prepares != 1 || a.pipeline.submits != 1 || a.chain.commits != 1 || a.claims.completes != 1 {
		t.Fatalf("company A recovery = prepare %d submit %d commit %d complete %d; want one each",
			a.pipeline.prepares, a.pipeline.submits, a.chain.commits, a.claims.completes)
	}

	// A new delivery of the same request after restart is a claim duplicate:
	// it is acknowledged without reading/rebuilding/submitting the invoice.
	publishProof(t, aNATS, proofSubject(a), msgA)
	rawReplay := fetchProof(t, subARecovered)
	handleDelivery(newProofStream(a), &tenantCtx{tenant: a.tenant}, a.tenant.DBName, a.branchID, rawReplay)
	if a.pipeline.prepares != 1 || a.pipeline.submits != 1 || a.chain.commits != 1 {
		t.Fatalf("company A replay did work again: prepare=%d submit=%d commit=%d",
			a.pipeline.prepares, a.pipeline.submits, a.chain.commits)
	}

	// B's acknowledged event survives its own daemon restart as completed,
	// while a fresh B bill can still be processed on B's private path.
	bNATS.close()
	bNATS = startProofNATS(t, b.natsDir, "proof-company-b")
	subBRecovered, err := bNATS.js.PullSubscribe(proofSubject(b), queue.OrderedConsumerName(b.tenant.DBName, b.branchID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subBRecovered.Fetch(1, nats.MaxWait(500*time.Millisecond)); !errors.Is(err, nats.ErrTimeout) {
		t.Fatalf("company B redelivered an acknowledged event after restart: %v", err)
	}
	bBill2 := b.store.create("company-b-only-2")
	msgB2 := proofMessage(b, bBill2.ID, "company-b-request-2")
	publishProof(t, bNATS, proofSubject(b), msgB2)
	rawB2 := fetchProof(t, subBRecovered)
	handleDelivery(newProofStream(b), &tenantCtx{tenant: b.tenant}, b.tenant.DBName, b.branchID, rawB2)
	if b.pipeline.prepares != 2 || b.pipeline.submits != 2 || b.chain.commits != 2 {
		t.Fatalf("company B post-restart processing = prepare %d submit %d commit %d; want two each",
			b.pipeline.prepares, b.pipeline.submits, b.chain.commits)
	}
	if a.pipeline.prepares != 1 || a.chain.commits != 1 {
		t.Fatalf("company A changed during company B restart proof: prepare=%d commit=%d",
			a.pipeline.prepares, a.chain.commits)
	}

	_ = ctx
}
