package credential

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zatca-go/zatca/config"
	zcrypto "github.com/zatca-go/zatca/crypto"
)

var (
	ErrCredentialKeyMismatch    = errors.New("ZATCA private key does not match certificate")
	ErrEnvironmentInvalid       = errors.New("ZATCA environment or endpoint is invalid")
	ErrSDKUnavailable           = errors.New("ZATCA SDK is unavailable")
	ErrWorkingPathUnavailable   = errors.New("ZATCA EGS working path is unavailable")
	ErrCertificateNotYetValid   = errors.New("ZATCA certificate is not yet valid")
	ErrPreflightInputInvalid    = errors.New("ZATCA credential preflight input is invalid")
	ErrPreflightGateUnavailable = errors.New("ZATCA credential preflight gate is unavailable")
)

// PreflightInput contains the state and runtime configuration that must be
// validated before an EGS can reserve a sequence number or process a
// document. It intentionally contains no provider client and performs no
// network or SDK operation.
type PreflightInput struct {
	Identity     Identity
	State        State
	Material     Material
	Config       *config.Config
	WorkingPaths []string
}

// PreflightGate validates all local prerequisites for document processing.
// SDKReady and PathCheck are injected so tests can prove the gate fails
// without invoking provider or filesystem side effects.
type PreflightGate struct {
	Now       func() time.Time
	SDKReady  func() error
	PathCheck func(string) error
}

func (g *PreflightGate) now() time.Time {
	if g != nil && g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Check validates a credential set without allocating ICV, signing XML, or
// contacting ZATCA.
func (g *PreflightGate) Check(input PreflightInput) error {
	if g == nil {
		return ErrPreflightGateUnavailable
	}
	if err := input.Identity.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrPreflightInputInvalid, err)
	}
	if input.State.Identity != (Identity{}) && input.State.Identity != input.Identity {
		return fmt.Errorf("%w: canonical credential identity does not match request", ErrPreflightInputInvalid)
	}

	switch {
	case input.State.Status == StatusRevoked || input.State.RevocationState == RevocationRevoked:
		return fmt.Errorf("%w: credential is revoked", ErrCredentialRevoked)
	case input.State.Status == StatusExpired:
		return fmt.Errorf("%w: canonical credential state is expired", ErrCredentialExpired)
	case input.State.RevocationState == RevocationPending:
		return fmt.Errorf("%w: revocation is pending", ErrCredentialPending)
	case input.State.RenewalState == RenewalPending:
		return fmt.Errorf("%w: renewal is pending", ErrCredentialPending)
	case input.State.RenewalState == RenewalFailed:
		return fmt.Errorf("%w: renewal failed: %s", ErrRenewalFailed, input.State.LastError)
	case input.State.Status != StatusActive:
		return fmt.Errorf("%w: canonical credential state is %q", ErrCredentialInvalid, input.State.Status)
	case strings.TrimSpace(input.State.CertificateSerial) == "":
		return fmt.Errorf("%w: canonical certificate serial is missing", ErrCredentialInvalid)
	case input.State.CertificateExpiresAt == nil:
		return fmt.Errorf("%w: canonical certificate expiry is missing", ErrCredentialInvalid)
	}

	now := g.now()
	if !input.State.CertificateExpiresAt.After(now) {
		return fmt.Errorf("%w: canonical certificate expired at %s",
			ErrCredentialExpired, input.State.CertificateExpiresAt.UTC().Format(time.RFC3339))
	}

	if err := validateMaterialPresence(input.Material); err != nil {
		return err
	}
	if err := validateEnvironment(input.Config); err != nil {
		return err
	}

	cert, err := parseMaterialCertificate(input.Material.Certificate)
	if err != nil {
		return fmt.Errorf("%w: parse certificate: %v", ErrCredentialInvalid, err)
	}
	privateKey, err := zcrypto.ParsePrivateKey([]byte(input.Material.PrivateKey))
	if err != nil {
		return fmt.Errorf("%w: parse private key: %v", ErrCredentialInvalid, err)
	}
	if cert.SerialNum == nil || cert.SerialNum.String() != input.State.CertificateSerial {
		return fmt.Errorf("%w: certificate serial does not match canonical state", ErrCredentialInvalid)
	}
	if now.Before(cert.NotBefore) {
		return fmt.Errorf("%w: certificate starts at %s",
			ErrCertificateNotYetValid, cert.NotBefore.UTC().Format(time.RFC3339))
	}
	if !now.Before(cert.NotAfter) {
		return fmt.Errorf("%w: certificate expired at %s",
			ErrCredentialExpired, cert.NotAfter.UTC().Format(time.RFC3339))
	}
	if input.State.CertificateExpiresAt.After(cert.NotAfter) {
		return fmt.Errorf("%w: canonical expiry is later than certificate expiry", ErrCredentialInvalid)
	}
	if !samePublicKey(&privateKey.PublicKey, cert.PublicKey) {
		return ErrCredentialKeyMismatch
	}

	if len(input.WorkingPaths) == 0 {
		return fmt.Errorf("%w: no per-EGS working paths configured", ErrWorkingPathUnavailable)
	}
	for _, path := range input.WorkingPaths {
		if err := g.checkPath(path); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrWorkingPathUnavailable, path, err)
		}
	}
	if g.SDKReady == nil {
		return fmt.Errorf("%w: SDK readiness check is not configured", ErrSDKUnavailable)
	}
	if err := g.SDKReady(); err != nil {
		return fmt.Errorf("%w: %v", ErrSDKUnavailable, err)
	}
	return nil
}

func validateMaterialPresence(material Material) error {
	switch {
	case strings.TrimSpace(material.Certificate) == "":
		return fmt.Errorf("%w: production certificate is missing", ErrCredentialInvalid)
	case strings.TrimSpace(material.PrivateKey) == "":
		return fmt.Errorf("%w: private key is missing", ErrCredentialInvalid)
	case strings.TrimSpace(material.Secret) == "":
		return fmt.Errorf("%w: production secret is missing", ErrCredentialInvalid)
	default:
		return nil
	}
}

func validateEnvironment(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("%w: ZATCA configuration is missing", ErrEnvironmentInvalid)
	}
	env, err := config.ParseEnv(string(cfg.Env))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEnvironmentInvalid, err)
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.BaseURL()), "/")
	expected := strings.TrimRight(env.BaseURL(), "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("%w: endpoint %q is not a valid HTTPS URL", ErrEnvironmentInvalid, endpoint)
	}
	if endpoint != expected {
		return fmt.Errorf("%w: endpoint %q does not match %s", ErrEnvironmentInvalid, endpoint, expected)
	}
	return nil
}

func parseMaterialCertificate(value string) (*zcrypto.CertInfo, error) {
	cert, err := zcrypto.ParseCertificate(value)
	if err == nil {
		return cert, nil
	}
	return zcrypto.ParseCertificatePEM([]byte(value))
}

func samePublicKey(left, right *ecdsa.PublicKey) bool {
	if left == nil || right == nil || left.X == nil || left.Y == nil || right.X == nil || right.Y == nil {
		return false
	}
	return left.X.Cmp(right.X) == 0 && left.Y.Cmp(right.Y) == 0
}

func (g *PreflightGate) checkPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path is empty")
	}
	if g.PathCheck != nil {
		return g.PathCheck(path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("path is not a directory")
	}
	probe, err := os.OpenFile(filepath.Join(path, ".zatca-credential-preflight"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("path is not writable: %w", err)
	}
	probeName := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(probeName)
		return fmt.Errorf("close path probe: %w", closeErr)
	}
	if err := os.Remove(probeName); err != nil {
		return fmt.Errorf("remove path probe: %w", err)
	}
	return nil
}
