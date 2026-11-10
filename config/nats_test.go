package config

import (
	"strings"
	"testing"
	"time"
)

func validNATSConfig(mode NATSMode) NATSConfig {
	return NATSConfig{
		Mode:               mode,
		URL:                "nats://nats-user:nats-password@nats.example:4222",
		Username:           "nats-user",
		Password:           "nats-password",
		PrivateNetworkOnly: true,
		Port:               4222,
		MonitorPort:        0,
		DataDir:            "nats-data",
		AckWait:            time.Minute,
		MaxDeliver:         10,
		ReconcileInterval:  time.Minute,
	}
}

func TestParseNATSModeRequiresAnExplicitSupportedMode(t *testing.T) {
	for _, value := range []string{"external", "EXTERNAL", " embedded "} {
		if _, err := ParseNATSMode(value); err != nil {
			t.Fatalf("ParseNATSMode(%q): %v", value, err)
		}
	}
	if _, err := ParseNATSMode(""); err == nil {
		t.Fatal("empty NATS mode was accepted")
	}
}

func TestExternalNATSRequiresAURL(t *testing.T) {
	cfg := validNATSConfig(NATSExternal)
	cfg.URL = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("external NATS without a URL was accepted")
	}
}

func TestEmbeddedNATSDoesNotNeedAnExternalURL(t *testing.T) {
	cfg := validNATSConfig(NATSEmbedded)
	cfg.URL = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("embedded NATS was rejected without an external URL: %v", err)
	}
}

func TestEmbeddedNATSRequiresCredentialsUnlessExplicitlyUnauthenticated(t *testing.T) {
	cfg := validNATSConfig(NATSEmbedded)
	cfg.PrivateNetworkOnly = false
	cfg.Username = ""
	cfg.Password = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("embedded NATS without credentials was accepted")
	}

	cfg.AllowUnauthenticated = true
	cfg.LocalTestMode = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit local unauthenticated embedded NATS was rejected: %v", err)
	}
}

func TestExternalNATSAcceptsCredentialsInURL(t *testing.T) {
	cfg := validNATSConfig(NATSExternal)
	cfg.Username = ""
	cfg.Password = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("external NATS URL credentials were rejected: %v", err)
	}
}

func TestNATSConfigRejectsPartialCredentialsAndExternalUnauthenticatedOptIn(t *testing.T) {
	cfg := validNATSConfig(NATSEmbedded)
	cfg.Password = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "set together") {
		t.Fatalf("Validate() = %v; want a paired-credential error", err)
	}

	cfg = validNATSConfig(NATSExternal)
	cfg.AllowUnauthenticated = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "local test mode") {
		t.Fatalf("Validate() = %v; want local-test-only unauthenticated error", err)
	}
}

func TestNATSConfigRejectsInvalidURLsAndLimits(t *testing.T) {
	cfg := validNATSConfig(NATSExternal)
	cases := []struct {
		name string
		edit func(*NATSConfig)
		want string
	}{
		{"wrong scheme", func(c *NATSConfig) { c.URL = "http://nats.example:4222" }, "nats://"},
		{"missing host", func(c *NATSConfig) { c.URL = "nats://" }, "host"},
		{"zero max deliveries", func(c *NATSConfig) { c.MaxDeliver = 0 }, "max deliveries"},
		{"zero reconcile interval", func(c *NATSConfig) { c.ReconcileInterval = 0 }, "reconciliation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := cfg
			tc.edit(&current)
			if err := current.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v; want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestNATSConfigRequiresPrivateNetworkForPlainExternalTransport(t *testing.T) {
	cfg := validNATSConfig(NATSExternal)
	cfg.PrivateNetworkOnly = false
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "private-network-only") {
		t.Fatalf("Validate() = %v; want private-network-only error", err)
	}

	cfg.URL = "tls://nats.example:4222"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("TLS external NATS was rejected: %v", err)
	}
}

func TestNATSConfigRequiresTLSFilesTogether(t *testing.T) {
	cfg := validNATSConfig(NATSEmbedded)
	cfg.PrivateNetworkOnly = false
	cfg.TLSCertFile = "server.crt"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "certificate and key") {
		t.Fatalf("Validate() = %v; want certificate/key pairing error", err)
	}

	cfg.TLSKeyFile = "server.key"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "CA file") {
		t.Fatalf("Validate() = %v; want CA file error", err)
	}

	cfg.TLSCAFile = "ca.crt"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("complete embedded TLS configuration was rejected: %v", err)
	}
}

func TestNATSConfigRequiresExplicitLocalTestMode(t *testing.T) {
	cfg := validNATSConfig(NATSEmbedded)
	cfg.PrivateNetworkOnly = false
	cfg.Username = ""
	cfg.Password = ""
	cfg.AllowUnauthenticated = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "local test mode") {
		t.Fatalf("Validate() = %v; want explicit local-test error", err)
	}
}
