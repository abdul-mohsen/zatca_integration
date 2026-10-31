package credential

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zatca-go/zatca/config"
)

type preflightMaterial struct {
	privateKey  string
	certificate string
	serial      string
}

func newPreflightMaterial(t *testing.T, notBefore, notAfter time.Time) preflightMaterial {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "preflight-test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return preflightMaterial{
		privateKey:  string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		certificate: base64.StdEncoding.EncodeToString(der),
		serial:      template.SerialNumber.String(),
	}
}

func newPreflightInput(t *testing.T) (PreflightInput, preflightMaterial) {
	t.Helper()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	material := newPreflightMaterial(t, now.Add(-time.Hour), now.Add(time.Hour))
	workingPath := t.TempDir()
	identity := Identity{
		TenantID:       42,
		BranchID:       7,
		EGSSequenceID:  "egs-7",
		CredentialType: CredentialTypeProduction,
	}
	expiresAt := now.Add(time.Hour)
	return PreflightInput{
		Identity: identity,
		State: State{
			Identity:             identity,
			CertificateSerial:    material.serial,
			CertificateExpiresAt: &expiresAt,
			Status:               StatusActive,
			RenewalState:         RenewalNone,
			RevocationState:      RevocationNone,
		},
		Material: Material{
			PrivateKey:  material.privateKey,
			Certificate: material.certificate,
			Secret:      "production-secret",
		},
		Config: &config.Config{
			Env:      config.Sandbox,
			XMLDir:   workingPath,
			TenantID: "tenant-42",
		},
		WorkingPaths: []string{workingPath},
	}, material
}

func newPreflightGate(now time.Time) *PreflightGate {
	return &PreflightGate{
		Now:       func() time.Time { return now },
		SDKReady:  func() error { return nil },
		PathCheck: func(path string) error { _, err := os.Stat(path); return err },
	}
}

func TestPreflightMissingCredentials(t *testing.T) {
	input, _ := newPreflightInput(t)
	input.Material = Material{}

	err := newPreflightGate(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)).Check(input)
	if !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("Check() error = %v, want ErrCredentialInvalid", err)
	}
}

func TestPreflightExpiredCertificate(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	input, material := newPreflightInput(t)
	expired := newPreflightMaterial(t, now.Add(-2*time.Hour), now.Add(-time.Minute))
	input.Material.PrivateKey = expired.privateKey
	input.Material.Certificate = expired.certificate
	input.State.CertificateSerial = expired.serial
	input.State.CertificateExpiresAt = timePtr(now.Add(time.Hour))
	_ = material

	err := newPreflightGate(now).Check(input)
	if !errors.Is(err, ErrCredentialExpired) {
		t.Fatalf("Check() error = %v, want ErrCredentialExpired", err)
	}
}

func TestPreflightRevokedStatus(t *testing.T) {
	input, _ := newPreflightInput(t)
	input.State.Status = StatusRevoked
	input.State.RevocationState = RevocationRevoked

	err := newPreflightGate(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)).Check(input)
	if !errors.Is(err, ErrCredentialRevoked) {
		t.Fatalf("Check() error = %v, want ErrCredentialRevoked", err)
	}
}

func TestPreflightPrivateKeyCertificateMismatch(t *testing.T) {
	input, _ := newPreflightInput(t)
	other := newPreflightMaterial(t, time.Date(2026, 10, 31, 11, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC))
	input.Material.PrivateKey = other.privateKey

	err := newPreflightGate(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)).Check(input)
	if !errors.Is(err, ErrCredentialKeyMismatch) {
		t.Fatalf("Check() error = %v, want ErrCredentialKeyMismatch", err)
	}
}

func TestPreflightInvalidEnvironment(t *testing.T) {
	input, _ := newPreflightInput(t)
	input.Config.Env = config.Environment("invalid")

	err := newPreflightGate(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)).Check(input)
	if !errors.Is(err, ErrEnvironmentInvalid) {
		t.Fatalf("Check() error = %v, want ErrEnvironmentInvalid", err)
	}
}

func TestPreflightInvalidEndpoint(t *testing.T) {
	input, _ := newPreflightInput(t)
	input.Config.Endpoint = "https://example.test/zatca"

	err := newPreflightGate(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)).Check(input)
	if !errors.Is(err, ErrEnvironmentInvalid) {
		t.Fatalf("Check() error = %v, want ErrEnvironmentInvalid", err)
	}
}

func TestPreflightSDKUnavailable(t *testing.T) {
	input, _ := newPreflightInput(t)
	gate := newPreflightGate(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	gate.SDKReady = func() error { return errors.New("shared SDK is unavailable") }

	err := gate.Check(input)
	if !errors.Is(err, ErrSDKUnavailable) {
		t.Fatalf("Check() error = %v, want ErrSDKUnavailable", err)
	}
}

func TestPreflightValidCredentials(t *testing.T) {
	input, _ := newPreflightInput(t)

	if err := newPreflightGate(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)).Check(input); err != nil {
		t.Fatalf("Check() error = %v, want nil", err)
	}
}

func TestPreflightMissingWorkingPath(t *testing.T) {
	input, _ := newPreflightInput(t)
	input.WorkingPaths = []string{filepath.Join(input.Config.XMLDir, "missing-egs")}

	err := newPreflightGate(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)).Check(input)
	if !errors.Is(err, ErrWorkingPathUnavailable) {
		t.Fatalf("Check() error = %v, want ErrWorkingPathUnavailable", err)
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}
