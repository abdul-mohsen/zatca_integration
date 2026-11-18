package main

import (
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/config"
)

func TestOpenNATSEmbeddedUsesConfiguredCredentials(t *testing.T) {
	cfg := config.NATSConfig{
		Mode:                            config.NATSEmbedded,
		Username:                        "daemon-user",
		Password:                        "daemon-password",
		PrivateNetworkOnly:              true,
		PrivateNetworkFirewallConfirmed: true,
		SharedCredentialsAcknowledged:   true,
		SharedCredentialsApprovalID:     "TEST-123",
		SharedCredentialsExpires:        time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02"),
		Port:                            0,
		DataDir:                         t.TempDir(),
		AckWait:                         time.Minute,
		MaxDeliver:                      1,
		ReconcileInterval:               time.Minute,
	}
	runtime, err := openNATS(cfg, "", nil)
	if err != nil {
		t.Fatalf("openNATS() error = %v", err)
	}
	t.Cleanup(runtime.close)

	unauthenticated, err := nats.Connect(runtime.server.ClientURL(), nats.Timeout(time.Second))
	if err == nil {
		unauthenticated.Close()
		t.Fatal("unauthenticated embedded NATS connection succeeded")
	}

	authenticated, err := nats.Connect(
		runtime.server.ClientURL(),
		nats.UserInfo(cfg.Username, cfg.Password),
		nats.Timeout(time.Second),
	)
	if err != nil {
		t.Fatalf("authenticated embedded NATS connection failed: %v", err)
	}
	authenticated.Close()
}

func TestOpenNATSPrivateNetworkModeDoesNotInferTLSFromServerName(t *testing.T) {
	cfg := config.NATSConfig{
		Mode:                            config.NATSEmbedded,
		Username:                        "daemon-user",
		Password:                        "daemon-password",
		PrivateNetworkOnly:              true,
		PrivateNetworkFirewallConfirmed: true,
		SharedCredentialsAcknowledged:   true,
		SharedCredentialsApprovalID:     "TEST-123",
		SharedCredentialsExpires:        time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02"),
		TLSServerName:                   "nats",
		Port:                            0,
		DataDir:                         t.TempDir(),
		AckWait:                         time.Minute,
		MaxDeliver:                      1,
		ReconcileInterval:               time.Minute,
	}
	runtime, err := openNATS(cfg, "", nil)
	if err != nil {
		t.Fatalf("private-network plaintext NATS with a TLS server name failed: %v", err)
	}
	t.Cleanup(runtime.close)

	if !runtime.conn.IsConnected() {
		t.Fatal("private-network NATS connection is not connected")
	}
}

func TestOpenNATSPrivateMonitorStillRequiresPassword(t *testing.T) {
	cfg := config.NATSConfig{
		Mode:                            config.NATSEmbedded,
		Username:                        "daemon-user",
		Password:                        "daemon-password",
		PrivateNetworkOnly:              true,
		PrivateNetworkFirewallConfirmed: true,
		SharedCredentialsAcknowledged:   true,
		SharedCredentialsApprovalID:     "TEST-123",
		SharedCredentialsExpires:        time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02"),
		MonitorPort:                     8222,
		MonitorHost:                     "0.0.0.0",
		MonitorPrivateNetworkOnly:       true,
		Port:                            0,
		DataDir:                         t.TempDir(),
		AckWait:                         time.Minute,
		MaxDeliver:                      1,
		ReconcileInterval:               time.Minute,
	}
	if _, err := openNATS(cfg, "", nil); err == nil || !strings.Contains(err.Error(), "monitor password is required") {
		t.Fatalf("openNATS() error = %v; want monitor authentication required", err)
	}
}

func TestOpenNATSAllowsUnauthenticatedOnlyForExplicitLocalTestMode(t *testing.T) {
	cfg := config.NATSConfig{
		Mode:                 config.NATSEmbedded,
		AllowUnauthenticated: true,
		LocalTestMode:        true,
		Port:                 0,
		DataDir:              t.TempDir(),
		AckWait:              time.Minute,
		MaxDeliver:           1,
		ReconcileInterval:    time.Minute,
	}
	runtime, err := openNATS(cfg, "", nil)
	if err != nil {
		t.Fatalf("openNATS() error = %v", err)
	}
	t.Cleanup(runtime.close)

	conn, err := nats.Connect(runtime.server.ClientURL(), nats.Timeout(time.Second))
	if err != nil {
		t.Fatalf("explicit local unauthenticated test connection failed: %v", err)
	}
	conn.Close()
}

func TestMonitorListenHostDefaultsToLoopback(t *testing.T) {
	cfg := config.NATSConfig{}
	if got := monitorListenHost(cfg); got != "127.0.0.1" {
		t.Fatalf("monitorListenHost() = %q; want loopback default", got)
	}
}

func TestMonitorListenHostHonorsConfiguredHost(t *testing.T) {
	cfg := config.NATSConfig{MonitorHost: "0.0.0.0"}
	if got := monitorListenHost(cfg); got != "0.0.0.0" {
		t.Fatalf("monitorListenHost() = %q; want configured host", got)
	}
}
