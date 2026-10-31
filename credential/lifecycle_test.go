package credential

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeLifecycleStore struct {
	state            State
	expired          bool
	renewalFailed    string
	issued           *IssuedCredential
	revocationFailed string
	revoked          bool
}

func (s *fakeLifecycleStore) Load(context.Context, Identity) (State, error) {
	return s.state, nil
}

func (s *fakeLifecycleStore) MarkExpired(context.Context, Identity, time.Time) error {
	s.expired = true
	s.state.Status = StatusExpired
	return nil
}

func (s *fakeLifecycleStore) MarkRenewalFailed(_ context.Context, _ Identity, message string) error {
	s.renewalFailed = message
	s.state.RenewalState = RenewalFailed
	return nil
}

func (s *fakeLifecycleStore) RecordIssued(_ context.Context, _ Identity, issued IssuedCredential, _ time.Time) error {
	s.issued = &issued
	s.state.Status = StatusActive
	s.state.RenewalState = RenewalCompleted
	s.state.CertificateSerial = issued.CertificateSerial
	s.state.CertificateExpiresAt = &issued.CertificateExpiresAt
	return nil
}

func (s *fakeLifecycleStore) MarkRevocationFailed(_ context.Context, _ Identity, message string) error {
	s.revocationFailed = message
	return nil
}

func (s *fakeLifecycleStore) MarkRevoked(context.Context, Identity, string, time.Time) error {
	s.revoked = true
	s.state.Status = StatusRevoked
	s.state.RevocationState = RevocationRevoked
	return nil
}

type fakeMaterialStore struct {
	material  Material
	persisted *IssuedCredential
}

func (s *fakeMaterialStore) Load(context.Context, Identity) (Material, error) {
	return s.material, nil
}

func (s *fakeMaterialStore) Persist(_ context.Context, _ Identity, issued IssuedCredential) error {
	s.persisted = &issued
	return nil
}

type fakeRenewalProvider struct {
	called bool
	result IssuedCredential
	err    error
}

func (p *fakeRenewalProvider) Renew(context.Context, RenewalRequest) (IssuedCredential, error) {
	p.called = true
	return p.result, p.err
}

type fakeRevocationProvider struct {
	called bool
	err    error
}

func (p *fakeRevocationProvider) Revoke(context.Context, RevocationRequest) error {
	p.called = true
	return p.err
}

type fakeCache struct {
	invalidated []Identity
}

func (c *fakeCache) Invalidate(identity Identity) {
	c.invalidated = append(c.invalidated, identity)
}

func lifecycleIdentity() Identity {
	return Identity{
		TenantID:       42,
		BranchID:       7,
		EGSSequenceID:  "egs-7",
		CredentialType: CredentialTypeProduction,
	}
}

func TestLifecycleExpiredCredentialBlocksProviderCall(t *testing.T) {
	expiredAt := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	store := &fakeLifecycleStore{
		state: State{
			Identity:             lifecycleIdentity(),
			CertificateSerial:    "old",
			CertificateExpiresAt: &expiredAt,
			Status:               StatusActive,
			RenewalState:         RenewalNone,
			RevocationState:      RevocationNone,
		},
	}
	renewal := &fakeRenewalProvider{}
	lifecycle := &Lifecycle{
		Store:   store,
		Renewal: renewal,
		Now:     func() time.Time { return time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC) },
	}

	_, err := lifecycle.ReconcileAndEnsure(context.Background(), lifecycleIdentity())
	if !errors.Is(err, ErrCredentialExpired) {
		t.Fatalf("ReconcileAndEnsure() error = %v, want ErrCredentialExpired", err)
	}
	if !store.expired {
		t.Fatal("expired credential was not persisted as expired")
	}
	if renewal.called {
		t.Fatal("expired credential invoked the renewal provider")
	}
}

func TestLifecycleRenewalPersistsRotationAndInvalidatesCache(t *testing.T) {
	expiresAt := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	store := &fakeLifecycleStore{
		state: State{
			Identity:             lifecycleIdentity(),
			CertificateSerial:    "old",
			CertificateExpiresAt: &expiresAt,
			Status:               StatusPending,
			RenewalState:         RenewalPending,
			RevocationState:      RevocationNone,
		},
	}
	materials := &fakeMaterialStore{material: Material{
		CSR:         "csr",
		PrivateKey:  "private-key",
		Certificate: "old-certificate",
		Secret:      "old-secret",
	}}
	renewal := &fakeRenewalProvider{result: IssuedCredential{
		Certificate:          "new-certificate",
		Secret:               "new-secret",
		CertificateSerial:    "new",
		CertificateExpiresAt: expiresAt.Add(365 * 24 * time.Hour),
	}}
	cache := &fakeCache{}
	lifecycle := &Lifecycle{
		Store:     store,
		Materials: materials,
		Renewal:   renewal,
		Cache:     cache,
		Now:       func() time.Time { return time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC) },
	}

	state, err := lifecycle.ReconcileAndEnsure(context.Background(), lifecycleIdentity())
	if err != nil {
		t.Fatalf("ReconcileAndEnsure() error = %v", err)
	}
	if !renewal.called {
		t.Fatal("renewal provider was not called")
	}
	if materials.persisted == nil || materials.persisted.Certificate != "new-certificate" {
		t.Fatal("renewed material was not persisted")
	}
	if store.issued == nil || store.issued.CertificateSerial != "new" {
		t.Fatal("canonical issued state was not persisted")
	}
	if len(cache.invalidated) != 1 || cache.invalidated[0] != lifecycleIdentity() {
		t.Fatalf("cache invalidations = %#v, want one lifecycle identity", cache.invalidated)
	}
	if state.Status != StatusActive {
		t.Fatalf("state status = %q, want active", state.Status)
	}
}

func TestLifecycleRevocationPersistsAndInvalidatesCache(t *testing.T) {
	expiresAt := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	store := &fakeLifecycleStore{state: State{
		Identity:             lifecycleIdentity(),
		CertificateSerial:    "serial",
		CertificateExpiresAt: &expiresAt,
		Status:               StatusActive,
		RenewalState:         RenewalNone,
		RevocationState:      RevocationPending,
	}}
	materials := &fakeMaterialStore{material: Material{
		Certificate: "certificate",
		Secret:      "secret",
	}}
	revocation := &fakeRevocationProvider{}
	cache := &fakeCache{}
	lifecycle := &Lifecycle{
		Store:      store,
		Materials:  materials,
		Revocation: revocation,
		Cache:      cache,
		Now:        func() time.Time { return time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC) },
	}

	state, err := lifecycle.Reconcile(context.Background(), lifecycleIdentity())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !revocation.called {
		t.Fatal("revocation provider was not called")
	}
	if !store.revoked {
		t.Fatal("revoked state was not persisted")
	}
	if len(cache.invalidated) != 1 {
		t.Fatalf("cache invalidations = %d, want 1", len(cache.invalidated))
	}
	if state.Status != StatusRevoked {
		t.Fatalf("state status = %q, want revoked", state.Status)
	}
}

func TestLifecycleRevokedCredentialBlocksProviderCall(t *testing.T) {
	store := &fakeLifecycleStore{state: State{
		Identity:        lifecycleIdentity(),
		Status:          StatusRevoked,
		RevocationState: RevocationRevoked,
		RenewalState:    RenewalNone,
	}}
	renewal := &fakeRenewalProvider{}
	revocation := &fakeRevocationProvider{}
	lifecycle := &Lifecycle{Store: store, Renewal: renewal, Revocation: revocation}

	_, err := lifecycle.ReconcileAndEnsure(context.Background(), lifecycleIdentity())
	if !errors.Is(err, ErrCredentialRevoked) {
		t.Fatalf("ReconcileAndEnsure() error = %v, want ErrCredentialRevoked", err)
	}
	if renewal.called || revocation.called {
		t.Fatal("revoked credential invoked a provider")
	}
}

func TestLifecycleInvalidCredentialBlocksProvider(t *testing.T) {
	store := &fakeLifecycleStore{state: State{
		Identity:        lifecycleIdentity(),
		Status:          StatusActive,
		RenewalState:    RenewalNone,
		RevocationState: RevocationNone,
	}}
	renewal := &fakeRenewalProvider{}
	lifecycle := &Lifecycle{Store: store, Renewal: renewal}

	_, err := lifecycle.ReconcileAndEnsure(context.Background(), lifecycleIdentity())
	if !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("ReconcileAndEnsure() error = %v, want ErrCredentialInvalid", err)
	}
	if renewal.called {
		t.Fatal("invalid credential invoked a provider")
	}
}

func TestLifecycleFailedRenewalPersistsFailure(t *testing.T) {
	expiresAt := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	providerErr := errors.New("provider unavailable")
	store := &fakeLifecycleStore{state: State{
		Identity:             lifecycleIdentity(),
		CertificateSerial:    "old",
		CertificateExpiresAt: &expiresAt,
		Status:               StatusPending,
		RenewalState:         RenewalPending,
		RevocationState:      RevocationNone,
	}}
	materials := &fakeMaterialStore{material: Material{
		CSR: "csr", PrivateKey: "key", Certificate: "cert", Secret: "secret",
	}}
	renewal := &fakeRenewalProvider{err: providerErr}
	lifecycle := &Lifecycle{Store: store, Materials: materials, Renewal: renewal}

	_, err := lifecycle.Reconcile(context.Background(), lifecycleIdentity())
	if !errors.Is(err, ErrRenewalFailed) {
		t.Fatalf("Reconcile() error = %v, want ErrRenewalFailed", err)
	}
	if !renewal.called || store.renewalFailed == "" {
		t.Fatal("failed renewal was not called and persisted")
	}
}

func TestLifecycleFailedRevocationPersistsFailure(t *testing.T) {
	providerErr := errors.New("revocation unavailable")
	store := &fakeLifecycleStore{state: State{
		Identity:          lifecycleIdentity(),
		CertificateSerial: "serial",
		Status:            StatusActive,
		RevocationState:   RevocationPending,
	}}
	materials := &fakeMaterialStore{material: Material{Certificate: "cert", Secret: "secret"}}
	revocation := &fakeRevocationProvider{err: providerErr}
	lifecycle := &Lifecycle{Store: store, Materials: materials, Revocation: revocation}

	_, err := lifecycle.Reconcile(context.Background(), lifecycleIdentity())
	if !errors.Is(err, ErrRevocationFailed) {
		t.Fatalf("Reconcile() error = %v, want ErrRevocationFailed", err)
	}
	if !revocation.called || store.revocationFailed == "" {
		t.Fatal("failed revocation was not called and persisted")
	}
}
