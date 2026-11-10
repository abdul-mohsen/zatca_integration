package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// NATSMode selects who owns the NATS server used by the daemon.
//
// External is the safe default for a shared deployment. Embedded is an
// explicit development or single-daemon choice; it is never inferred from a
// missing URL.
type NATSMode string

const (
	NATSExternal NATSMode = "external"
	NATSEmbedded NATSMode = "embedded"
)

// ParseNATSMode validates the daemon's NATS ownership mode.
func ParseNATSMode(value string) (NATSMode, error) {
	mode := NATSMode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case NATSExternal, NATSEmbedded:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid NATS mode %q (must be external or embedded)", value)
	}
}

// NATSConfig contains the daemon's transport and delivery limits.
type NATSConfig struct {
	Mode                 NATSMode
	URL                  string
	Username             string
	Password             string
	AllowUnauthenticated bool
	LocalTestMode        bool
	PrivateNetworkOnly   bool
	TLSCertFile          string
	TLSKeyFile           string
	TLSCAFile            string
	TLSServerName        string
	Port                 int
	MonitorPort          int
	DataDir              string
	AckWait              time.Duration
	MaxDeliver           int
	ReconcileInterval    time.Duration
}

// Validate checks ownership-specific configuration before any process starts.
func (c NATSConfig) Validate() error {
	mode, err := ParseNATSMode(string(c.Mode))
	if err != nil {
		return err
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("NATS port must be between 0 and 65535")
	}
	if c.MonitorPort < 0 || c.MonitorPort > 65535 {
		return fmt.Errorf("NATS monitor port must be between 0 and 65535")
	}
	if c.AckWait <= 0 {
		return fmt.Errorf("NATS ACK wait must be positive")
	}
	if c.MaxDeliver < 1 {
		return fmt.Errorf("NATS max deliveries must be positive")
	}
	if c.ReconcileInterval <= 0 {
		return fmt.Errorf("tenant reconciliation interval must be positive")
	}
	hasUsername := strings.TrimSpace(c.Username) != ""
	hasPassword := strings.TrimSpace(c.Password) != ""
	if hasUsername != hasPassword {
		return fmt.Errorf("NATS username and password must be set together")
	}
	hasTLSCert := strings.TrimSpace(c.TLSCertFile) != ""
	hasTLSKey := strings.TrimSpace(c.TLSKeyFile) != ""
	if hasTLSCert != hasTLSKey {
		return fmt.Errorf("NATS TLS certificate and key must be set together")
	}
	if c.AllowUnauthenticated && (mode != NATSEmbedded || !c.LocalTestMode) {
		return fmt.Errorf("unauthenticated NATS is only allowed in explicit local test mode")
	}
	if mode == NATSExternal {
		if strings.TrimSpace(c.URL) == "" {
			return fmt.Errorf("NATS URL is required in external mode")
		}
		parsed, err := url.Parse(c.URL)
		if err != nil || parsed.Scheme != "nats" && parsed.Scheme != "tls" {
			return fmt.Errorf("NATS URL must use nats:// or tls:// in external mode")
		}
		if parsed.Host == "" {
			return fmt.Errorf("NATS URL must include a host in external mode")
		}
		urlHasAuth := parsed.User != nil && parsed.User.Username() != ""
		if !c.AllowUnauthenticated && !hasUsername && !urlHasAuth {
			return fmt.Errorf("NATS authentication is required in external mode")
		}
		if parsed.Scheme == "nats" && !c.PrivateNetworkOnly {
			return fmt.Errorf("plain nats:// external transport requires private-network-only mode")
		}
	}
	if mode == NATSEmbedded {
		if strings.TrimSpace(c.DataDir) == "" {
			return fmt.Errorf("NATS data directory is required in embedded mode")
		}
		if !c.AllowUnauthenticated && !hasUsername {
			return fmt.Errorf("NATS username and password are required in embedded mode")
		}
		if !c.AllowUnauthenticated && !hasTLSCert && !c.PrivateNetworkOnly {
			return fmt.Errorf("embedded production NATS requires TLS or private-network-only mode")
		}
		if hasTLSCert && strings.TrimSpace(c.TLSCAFile) == "" {
			return fmt.Errorf("NATS TLS CA file is required when TLS is enabled")
		}
	}
	return nil
}
