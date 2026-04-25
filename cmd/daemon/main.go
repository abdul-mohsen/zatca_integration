package main

import (
	"crypto/subtle"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/dbutil"
	"github.com/zatca-go/zatca/processor"
	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/secutil"
	"github.com/zatca-go/zatca/storeconfig"
	"github.com/zatca-go/zatca/tenant"
	"github.com/zatca-go/zatca/zatca"
)

func main() {
	masterDSN := flag.String("master-dsn", "", "Full DSN for zatca_master DB (alternative to individual flags)")
	baseDSN := flag.String("base-dsn", "", "Full base DSN without DB name (alternative to individual flags)")
	adminDSN := flag.String("admin-dsn", "", "Optional base DSN for admin user (onboarding). Falls back to base-dsn if unset")
	dbUser := flag.String("db-user", "", "Database username")
	dbPass := flag.String("db-pass", "", "Database password (safe for special characters)")
	dbHost := flag.String("db-host", "", "Database host")
	dbPort := flag.String("db-port", "3306", "Database port")
	masterDB := flag.String("master-db", "zatca_master", "Master database name")
	natsPort := flag.Int("nats-port", 4222, "Port for embedded NATS server")
	natsMonPort := flag.Int("nats-monitor", 8222, "Port for NATS HTTP monitoring (0 to disable)")
	natsDataDir := flag.String("nats-data", "./nats-data", "JetStream data directory")
	xmlDir := flag.String("xml-dir", "/data/zatca-xml", "Base directory for storing signed XML files")
	flag.Parse()

	// Env var fallbacks (for Docker / docker-compose)
	if *dbUser == "" {
		*dbUser = os.Getenv("DB_USER")
	}
	if *dbPass == "" {
		*dbPass = os.Getenv("DB_PASS")
	}
	if *dbHost == "" {
		*dbHost = os.Getenv("DB_HOST")
	}
	if p := os.Getenv("DB_PORT"); p != "" && *dbPort == "3306" {
		*dbPort = p
	}
	if m := os.Getenv("MASTER_DB"); m != "" && *masterDB == "zatca_master" {
		*masterDB = m
	}
	if *adminDSN == "" {
		if au, ap := os.Getenv("ADMIN_DB_USER"), os.Getenv("ADMIN_DB_PASS"); au != "" {
			host := *dbHost
			if h := os.Getenv("ADMIN_DB_HOST"); h != "" {
				host = h
			}
			*adminDSN = dbutil.FormatBaseDSN(au, ap, host, *dbPort)
		}
	}

	// Build DSNs: prefer explicit --master-dsn/--base-dsn, fall back to individual flags
	if *masterDSN == "" && *dbUser != "" && *dbHost != "" {
		*masterDSN = dbutil.FormatDSN(*dbUser, *dbPass, *dbHost, *dbPort, *masterDB)
	}
	if *baseDSN == "" && *dbUser != "" && *dbHost != "" {
		*baseDSN = dbutil.FormatBaseDSN(*dbUser, *dbPass, *dbHost, *dbPort)
	}
	if *masterDSN == "" || *baseDSN == "" {
		log.Fatal("Database connection required: use --master-dsn/--base-dsn OR --db-user/--db-pass/--db-host")
	}

	// Parse encryption key for sensitive DB columns (required)
	encKey, err := secutil.NewKey(os.Getenv("ENCRYPT_KEY"))
	if err != nil {
		log.Fatalf("ENCRYPT_KEY is required: %v (generate with: openssl rand -base64 32)", err)
	}

	// ── 1. Start embedded NATS with JetStream ──────────────────────────
	opts := &server.Options{
		Port:      *natsPort,
		JetStream: true,
		StoreDir:  *natsDataDir,
		NoLog:     true,
		NoSigs:    true,
	}
	if *natsMonPort > 0 {
		// Bind NATS monitoring to localhost only (not public)
		internalMonPort := *natsMonPort + 10000 // e.g. 18222
		opts.HTTPHost = "127.0.0.1"
		opts.HTTPPort = internalMonPort

		monPass := os.Getenv("MONITOR_PASS")
		if monPass == "" {
			log.Fatal("MONITOR_PASS is required when --nats-monitor is enabled (set to disable: --nats-monitor=0)")
		}

		// Auth-protected reverse proxy → internal NATS monitoring
		target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", internalMonPort))
		proxy := httputil.NewSingleHostReverseProxy(target)
		monMux := http.NewServeMux()
		monMux.Handle("/", basicAuth(proxy, monPass))
		go func() {
			addr := fmt.Sprintf(":%d", *natsMonPort)
			log.Printf("NATS monitoring (auth-protected) at http://0.0.0.0:%d", *natsMonPort)
			if err := http.ListenAndServe(addr, monMux); err != nil {
				log.Printf("WARN: monitoring server: %v", err)
			}
		}()
	}
	ns, err := server.NewServer(opts)
	if err != nil {
		log.Fatalf("nats server create: %v", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		log.Fatal("nats server not ready")
	}
	log.Printf("Embedded NATS running on port %d (JetStream dir: %s)", *natsPort, *natsDataDir)

	// ── 2. Connect to NATS ─────────────────────────────────────────────
	nc, err := nats.Connect(fmt.Sprintf("nats://localhost:%d", *natsPort))
	if err != nil {
		log.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	// Ensure the ZATCA stream exists
	_, err = js.AddStream(&nats.StreamConfig{
		Name:      queue.StreamName,
		Subjects:  []string{queue.SubjectAll},
		Retention: nats.WorkQueuePolicy,
		Storage:   nats.FileStorage,
		MaxAge:    7 * 24 * time.Hour, // keep messages for 7 days
	})
	if err != nil {
		log.Fatalf("add stream: %v", err)
	}
	log.Println("JetStream stream ready: ZATCA")

	// ── 3. Open master DB & get tenants ────────────────────────────────
	masterDBConn, err := sql.Open("mysql", *masterDSN)
	if err != nil {
		log.Fatalf("open master db: %v", err)
	}
	defer masterDBConn.Close()

	tenants, err := tenant.LoadAll(masterDBConn)
	if err != nil {
		log.Fatalf("load tenants: %v", err)
	}
	log.Printf("Loaded %d tenant(s)", len(tenants))

	// ── 4. Build per-tenant context ────────────────────────────────────
	tenantMap := map[string]*tenantCtx{} // dbName → context

	for _, t := range tenants {
		tdb, err := tenant.OpenDB(t, *baseDSN)
		if err != nil {
			log.Printf("WARN: skip tenant %s: %v", t.Name, err)
			continue
		}
		cfg := &config.Config{
			Env:      config.Environment(t.ZATCAEnv),
			XMLDir:   *xmlDir,
			TenantID: t.DBName,
		}
		tc := &tenantCtx{tenant: t, db: tdb, cfg: cfg, encKey: encKey}

		// Open separate admin connection for onboarding if admin-dsn is set
		if *adminDSN != "" {
			adb, err := tenant.OpenDB(t, *adminDSN)
			if err != nil {
				log.Printf("WARN: admin DB for tenant %s failed, onboarding will use runtime user: %v", t.Name, err)
			} else {
				tc.adminDB = adb
			}
		}

		tenantMap[t.DBName] = tc
		log.Printf("  Tenant %s (%s) ready", t.Name, t.DBName)
	}
	defer func() {
		for _, tc := range tenantMap {
			tc.db.Close()
			if tc.adminDB != nil {
				tc.adminDB.Close()
			}
		}
	}()

	// ── 5. Per-branch service caches (tenant × branch → zatca.Service) ─
	type branchEntry struct {
		svc *zatca.Service
		cfg *config.Config
	}
	branchCaches := map[string]map[int64]*branchEntry{} // dbName → branchID → entry
	var cacheMu sync.RWMutex

	getService := func(dbName string, branchID int64) (*zatca.Service, *config.Config, error) {
		cacheMu.RLock()
		if cache, ok := branchCaches[dbName]; ok {
			if entry, ok := cache[branchID]; ok {
				cacheMu.RUnlock()
				return entry.svc, entry.cfg, nil
			}
		}
		cacheMu.RUnlock()

		tc, ok := tenantMap[dbName]
		if !ok {
			return nil, nil, fmt.Errorf("unknown tenant db: %s", dbName)
		}

		bz, err := storeconfig.LoadBranchZATCA(tc.db, branchID, encKey)
		if err != nil {
			return nil, nil, fmt.Errorf("load branch %d ZATCA config: %w", branchID, err)
		}
		if !bz.IsRegistered() {
			return nil, nil, fmt.Errorf("branch %d not registered with ZATCA", branchID)
		}

		branchCfg := bz.ToConfig(tc.cfg)
		svc := bz.NewService(tc.cfg)

		cacheMu.Lock()
		if branchCaches[dbName] == nil {
			branchCaches[dbName] = map[int64]*branchEntry{}
		}
		branchCaches[dbName][branchID] = &branchEntry{svc: svc, cfg: branchCfg}
		cacheMu.Unlock()

		return svc, branchCfg, nil
	}

	// ── 6. Launch workers ──────────────────────────────────────────────
	var wg sync.WaitGroup

	// Discover active branches per tenant and start workers
	for dbName, tc := range tenantMap {
		branches, err := processor.QueryActiveBranches(tc.db)
		if err != nil {
			log.Printf("WARN: cannot query branches for %s: %v", dbName, err)
			continue
		}

		for _, branchID := range branches {
			markBranchActive(dbName, branchID) // register so onboard won't duplicate
			for _, docType := range []queue.DocType{queue.DocBill, queue.DocCredit, queue.DocDebit} {
				wg.Add(1)
				go func(dt queue.DocType, db string, bid int64) {
					defer wg.Done()
					runWorker(js, tenantMap, getService, dt, db, bid)
				}(docType, dbName, branchID)
			}
		}
		log.Printf("  Started %d workers for tenant %s (%d branches × 3 doc types)", len(branches)*3, tc.tenant.Name, len(branches))
		if len(branches) == 0 {
			log.Printf("  NOTE: No onboarded branches in %s — onboard a branch first, then bill workers will auto-start", tc.tenant.Name)
		}
	}

	// Start a single onboard worker per tenant (handles all branches for that tenant)
	for dbName := range tenantMap {
		wg.Add(1)
		go func(db string) {
			defer wg.Done()
			runOnboardWorker(js, tenantMap, getService, &wg, db)
		}(dbName)
	}

	// ── 7. Startup replay ──────────────────────────────────────────────
	for dbName, tc := range tenantMap {
		branches, err := processor.QueryActiveBranches(tc.db)
		if err != nil {
			continue
		}
		replayPending(js, tc.db, dbName, branches)
	}

	// ── 8. Wait for shutdown ───────────────────────────────────────────
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down...")

	nc.Drain()
	ns.Shutdown()
	ns.WaitForShutdown()
	log.Println("NATS shutdown complete")
}

// basicAuth wraps an http.Handler with HTTP Basic Authentication.
func basicAuth(next http.Handler, password string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="NATS Monitoring"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
