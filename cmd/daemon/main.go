package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/dbutil"
	"github.com/zatca-go/zatca/provider"
	"github.com/zatca-go/zatca/secutil"
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
	natsMode := flag.String("nats-mode", envOrDefault("NATS_MODE", string(config.NATSExternal)), "NATS ownership mode: external or embedded")
	natsURL := flag.String("nats-url", envOrDefault("NATS_URL", ""), "External NATS URL (required in external mode)")
	natsUser := flag.String("nats-user", envOrDefault("NATS_USER", ""), "NATS username")
	natsPassword := flag.String("nats-password", envOrDefault("NATS_PASSWORD", ""), "NATS password")
	natsAllowUnauthenticated := flag.Bool("nats-allow-unauthenticated", envTruthy("NATS_ALLOW_UNAUTHENTICATED"), "Allow unauthenticated embedded NATS for explicit local/test use")
	natsLocalTestMode := flag.Bool("nats-local-test-mode", envTruthy("NATS_LOCAL_TEST_MODE"), "Enable local/test-only NATS settings")
	natsPrivateNetworkOnly := flag.Bool("nats-private-network-only", envTruthy("NATS_PRIVATE_NETWORK_ONLY"), "Declare that plain nats:// transport is private-network-only")
	natsTLSCert := flag.String("nats-tls-cert", envOrDefault("NATS_TLS_CERT_FILE", ""), "NATS TLS server/client certificate file")
	natsTLSKey := flag.String("nats-tls-key", envOrDefault("NATS_TLS_KEY_FILE", ""), "NATS TLS server/client private key file")
	natsTLSCA := flag.String("nats-tls-ca", envOrDefault("NATS_TLS_CA_FILE", ""), "NATS TLS CA certificate bundle")
	natsTLSServerName := flag.String("nats-tls-server-name", envOrDefault("NATS_TLS_SERVER_NAME", ""), "NATS TLS server name")
	natsPort := flag.Int("nats-port", 4222, "Port for embedded NATS server")
	natsMonPort := flag.Int("nats-monitor", 0, "Port for embedded NATS HTTP monitoring (0 to disable)")
	natsDataDir := flag.String("nats-data", "./nats-data", "JetStream data directory")
	natsAckWait := flag.Duration("nats-ack-wait", 5*time.Minute, "JetStream delivery ACK wait")
	natsMaxDeliver := flag.Int("nats-max-deliver", 10, "Maximum delivery attempts before local terminal failure")
	tenantReconcileInterval := flag.Duration("tenant-reconcile-interval", time.Minute, "Tenant and branch discovery interval")
	providerEndpoint := flag.String("provider-endpoint", envOrDefault("ZATCA_PROVIDER_ENDPOINT", ""), "Optional ZATCA provider endpoint override")
	providerMode := flag.String("provider-mode", envOrDefault("ZATCA_PROVIDER_MODE", string(providerModeReal)), "ZATCA provider mode: real or local")
	providerScenario := flag.String("provider-scenario", envOrDefault("ZATCA_PROVIDER_SCENARIO", string(provider.ScenarioAccepted)), "Local provider submit scenario")
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

	mode, err := config.ParseNATSMode(*natsMode)
	if err != nil {
		log.Fatal(err)
	}
	natsCfg := config.NATSConfig{
		Mode:                 mode,
		URL:                  *natsURL,
		Username:             *natsUser,
		Password:             *natsPassword,
		AllowUnauthenticated: *natsAllowUnauthenticated,
		LocalTestMode:        *natsLocalTestMode,
		PrivateNetworkOnly:   *natsPrivateNetworkOnly,
		TLSCertFile:          *natsTLSCert,
		TLSKeyFile:           *natsTLSKey,
		TLSCAFile:            *natsTLSCA,
		TLSServerName:        *natsTLSServerName,
		Port:                 *natsPort,
		MonitorPort:          *natsMonPort,
		DataDir:              *natsDataDir,
		AckWait:              *natsAckWait,
		MaxDeliver:           *natsMaxDeliver,
		ReconcileInterval:    *tenantReconcileInterval,
	}
	var reconciler *daemonReconciler
	readiness := func(ctx context.Context) error {
		if reconciler == nil {
			return errors.New("daemon reconciliation has not started")
		}
		return reconciler.checkReadiness(ctx)
	}
	natsRT, err := openNATS(natsCfg, os.Getenv("MONITOR_PASS"), readiness)
	if err != nil {
		log.Fatalf("NATS startup: %v", err)
	}
	defer natsRT.close()
	js, err := natsRT.conn.JetStream()
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	if err := ensureStream(js, mode); err != nil {
		log.Fatalf("ZATCA stream: %v", err)
	}
	log.Printf("NATS ready in %s mode", mode)

	providerFactory, err := newProviderFactory(*providerMode, *providerScenario)
	if err != nil {
		log.Fatalf("ZATCA provider configuration: %v", err)
	}

	// ── 2. Open master DB; tenant discovery is reconciled continuously ──
	masterDBConn, err := sql.Open("mysql", *masterDSN)
	if err != nil {
		log.Fatalf("open master db: %v", err)
	}
	// Recycle connections to avoid stale-idle broken-pipe warnings
	// (Docker bridge / NAT drops long-idle TCP).
	masterDBConn.SetConnMaxLifetime(3 * time.Minute)
	masterDBConn.SetConnMaxIdleTime(1 * time.Minute)
	masterDBConn.SetMaxOpenConns(5)
	masterDBConn.SetMaxIdleConns(2)
	defer masterDBConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	reconciler = newDaemonReconcilerWithProviderFactory(
		ctx,
		masterDBConn,
		*baseDSN,
		*adminDSN,
		encKey,
		natsRT.conn,
		js,
		&wg,
		*natsAckWait,
		*natsMaxDeliver,
		providerFactory,
	)
	reconciler.providerEndpoint = *providerEndpoint
	if err := reconciler.reconcile(ctx); err != nil {
		log.Fatalf("initial tenant reconciliation: %v", err)
	}
	go reconciler.loop(*tenantReconcileInterval)

	// ── 3. Wait for shutdown ───────────────────────────────────────────
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down...")
	reconciler.stop()
	log.Println("ZATCA daemon shutdown complete")
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

func envTruthy(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
