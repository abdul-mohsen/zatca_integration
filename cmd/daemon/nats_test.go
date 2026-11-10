package main

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/config"
)

func TestOpenNATSEmbeddedUsesConfiguredCredentials(t *testing.T) {
	cfg := config.NATSConfig{
		Mode:               config.NATSEmbedded,
		Username:           "daemon-user",
		Password:           "daemon-password",
		PrivateNetworkOnly: true,
		Port:               0,
		DataDir:            t.TempDir(),
		AckWait:            time.Minute,
		MaxDeliver:         1,
		ReconcileInterval:  time.Minute,
	}
	runtime, err := openNATS(cfg, "")
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
	runtime, err := openNATS(cfg, "")
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
