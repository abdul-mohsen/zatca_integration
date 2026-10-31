package credential

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	CredentialTypeCompliance = "compliance"
	CredentialTypeProduction = "production"
)

type Status string

const (
	StatusPending Status = "pending"
	StatusActive  Status = "active"
	StatusExpired Status = "expired"
	StatusRevoked Status = "revoked"
)

type RenewalState string

const (
	RenewalNone      RenewalState = "none"
	RenewalPending   RenewalState = "pending"
	RenewalCompleted RenewalState = "completed"
	RenewalFailed    RenewalState = "failed"
)

type RevocationState string

const (
	RevocationNone    RevocationState = "none"
	RevocationPending RevocationState = "pending"
	RevocationRevoked RevocationState = "revoked"
)

var (
	ErrCredentialNotFound         = errors.New("canonical ZATCA credential not found")
	ErrCredentialExpired          = errors.New("ZATCA credential is expired")
	ErrCredentialRevoked          = errors.New("ZATCA credential is revoked")
	ErrCredentialInvalid          = errors.New("ZATCA credential is invalid")
	ErrCredentialPending          = errors.New("ZATCA credential lifecycle transition is pending")
	ErrRenewalFailed              = errors.New("ZATCA credential renewal failed")
	ErrRevocationFailed           = errors.New("ZATCA credential revocation failed")
	ErrLifecycleStoreRequired     = errors.New("ZATCA credential lifecycle store is required")
	ErrMaterialStoreRequired      = errors.New("ZATCA credential material store is required")
	ErrRenewalProviderRequired    = errors.New("ZATCA credential renewal provider is required")
	ErrRevocationProviderRequired = errors.New("ZATCA credential revocation provider is required")
)

// Identity is the canonical owner key from the backend CSID contract.
type Identity struct {
	TenantID       uint64
	BranchID       int64
	EGSSequenceID  string
	CredentialType string
}

func (i Identity) Validate() error {
	switch {
	case i.TenantID == 0:
		return errors.New("credential tenant_id is required")
	case i.BranchID <= 0:
		return errors.New("credential branch_id is required")
	case strings.TrimSpace(i.EGSSequenceID) == "":
		return errors.New("credential egs_sequence_id is required")
	case i.CredentialType != CredentialTypeCompliance && i.CredentialType != CredentialTypeProduction:
		return fmt.Errorf("credential type %q is invalid", i.CredentialType)
	default:
		return nil
	}
}

// State contains the canonical, non-secret lifecycle state owned by the
// backend contract. Credential material remains in branch_zatca_config.
type State struct {
	Identity              Identity
	CertificateSerial     string
	CertificateExpiresAt  *time.Time
	Status                Status
	RenewalState          RenewalState
	RevocationState       RevocationState
	RenewalRequestedAt    *time.Time
	RenewedAt             *time.Time
	RevocationRequestedAt *time.Time
	RevokedAt             *time.Time
	RevocationReason      string
	LastError             string
}

// Material is the secret-bearing legacy branch credential projection used to
// authenticate lifecycle provider calls. It is never stored in the canonical
// lifecycle table.
type Material struct {
	CSR         string
	PrivateKey  string
	Certificate string
	Secret      string
	RequestID   string
}

func (m Material) validateForRenewal() error {
	switch {
	case strings.TrimSpace(m.CSR) == "":
		return fmt.Errorf("%w: CSR is missing", ErrCredentialInvalid)
	case strings.TrimSpace(m.PrivateKey) == "":
		return fmt.Errorf("%w: private key is missing", ErrCredentialInvalid)
	case strings.TrimSpace(m.Certificate) == "":
		return fmt.Errorf("%w: certificate is missing", ErrCredentialInvalid)
	case strings.TrimSpace(m.Secret) == "":
		return fmt.Errorf("%w: provider secret is missing", ErrCredentialInvalid)
	default:
		return nil
	}
}

func (m Material) validateForRevocation() error {
	switch {
	case strings.TrimSpace(m.Certificate) == "":
		return fmt.Errorf("%w: certificate is missing", ErrCredentialInvalid)
	case strings.TrimSpace(m.Secret) == "":
		return fmt.Errorf("%w: provider secret is missing", ErrCredentialInvalid)
	default:
		return nil
	}
}

// IssuedCredential is the provider result required to rotate both canonical
// metadata and the secret-bearing branch credential projection.
type IssuedCredential struct {
	Certificate          string
	Secret               string
	PrivateKey           string
	CSR                  string
	RequestID            string
	CertificateSerial    string
	CertificateExpiresAt time.Time
}

func (i IssuedCredential) Validate(now time.Time) error {
	switch {
	case strings.TrimSpace(i.Certificate) == "":
		return fmt.Errorf("%w: renewed certificate is missing", ErrCredentialInvalid)
	case strings.TrimSpace(i.Secret) == "":
		return fmt.Errorf("%w: renewed provider secret is missing", ErrCredentialInvalid)
	case strings.TrimSpace(i.CertificateSerial) == "":
		return fmt.Errorf("%w: renewed certificate serial is missing", ErrCredentialInvalid)
	case i.CertificateExpiresAt.IsZero():
		return fmt.Errorf("%w: renewed certificate expiry is missing", ErrCredentialInvalid)
	case !i.CertificateExpiresAt.After(now):
		return fmt.Errorf("%w: renewed certificate is not valid in the future", ErrCredentialInvalid)
	default:
		return nil
	}
}

type RenewalRequest struct {
	Identity Identity
	State    State
	Material Material
}

type RevocationRequest struct {
	Identity          Identity
	State             State
	Material          Material
	CertificateSerial string
	Reason            string
}

// Store persists canonical lifecycle state using the backend contract.
type Store interface {
	Load(context.Context, Identity) (State, error)
	MarkExpired(context.Context, Identity, time.Time) error
	MarkRenewalFailed(context.Context, Identity, string) error
	RecordIssued(context.Context, Identity, IssuedCredential, time.Time) error
	MarkRevocationFailed(context.Context, Identity, string) error
	MarkRevoked(context.Context, Identity, string, time.Time) error
}

// MaterialStore loads and rotates the secret-bearing branch credential
// projection. Implementations must encrypt secret/private-key values before
// writing them to the database.
type MaterialStore interface {
	Load(context.Context, Identity) (Material, error)
	Persist(context.Context, Identity, IssuedCredential) error
}

// RenewalProvider is the only boundary through which a CSID renewal may be
// requested.
type RenewalProvider interface {
	Renew(context.Context, RenewalRequest) (IssuedCredential, error)
}

// RevocationProvider is the only boundary through which a CSID revocation may
// be requested.
type RevocationProvider interface {
	Revoke(context.Context, RevocationRequest) error
}

type CacheInvalidator interface {
	Invalidate(Identity)
}

type Lifecycle struct {
	Store      Store
	Materials  MaterialStore
	Renewal    RenewalProvider
	Revocation RevocationProvider
	Cache      CacheInvalidator
	Now        func() time.Time
}

func (l *Lifecycle) now() time.Time {
	if l != nil && l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Lifecycle) load(ctx context.Context, identity Identity) (State, error) {
	if l == nil || l.Store == nil {
		return State{}, ErrLifecycleStoreRequired
	}
	if err := identity.Validate(); err != nil {
		return State{}, err
	}
	state, err := l.Store.Load(ctx, identity)
	if err != nil {
		return State{}, err
	}
	if state.Identity == (Identity{}) {
		state.Identity = identity
	}
	return state, nil
}

// EnsureUsable loads and validates the canonical state without invoking any
// provider. It is intentionally safe to call on every service-cache lookup.
func (l *Lifecycle) EnsureUsable(ctx context.Context, identity Identity) (State, error) {
	state, err := l.load(ctx, identity)
	if err != nil {
		return State{}, err
	}
	if state.RevocationState == RevocationRevoked || state.Status == StatusRevoked {
		return state, fmt.Errorf("%w: %s", ErrCredentialRevoked, identity.EGSSequenceID)
	}
	if state.RenewalState == RenewalFailed {
		return state, fmt.Errorf("%w: %s", ErrRenewalFailed, state.LastError)
	}
	if state.RevocationState == RevocationPending || state.RenewalState == RenewalPending {
		return state, fmt.Errorf("%w: %s", ErrCredentialPending, identity.EGSSequenceID)
	}
	if state.Status == StatusExpired {
		return state, fmt.Errorf("%w: %s", ErrCredentialExpired, identity.EGSSequenceID)
	}
	if state.Status != StatusActive ||
		strings.TrimSpace(state.CertificateSerial) == "" ||
		state.CertificateExpiresAt == nil {
		return state, fmt.Errorf("%w: canonical state is not active", ErrCredentialInvalid)
	}
	if !state.CertificateExpiresAt.After(l.now()) {
		if err := l.Store.MarkExpired(ctx, identity, l.now()); err != nil {
			return state, fmt.Errorf("mark credential expired: %w", err)
		}
		state.Status = StatusExpired
		return state, fmt.Errorf("%w: %s", ErrCredentialExpired, identity.EGSSequenceID)
	}
	return state, nil
}

// Reconcile processes at most one pending revocation or renewal transition.
// Provider calls are made only after the canonical state and required
// credential material have been loaded and validated.
func (l *Lifecycle) Reconcile(ctx context.Context, identity Identity) (State, error) {
	state, err := l.load(ctx, identity)
	if err != nil {
		return State{}, err
	}
	if state.Status == StatusRevoked || state.RevocationState == RevocationRevoked {
		return state, nil
	}

	if state.RevocationState == RevocationPending {
		if err := l.reconcileRevocation(ctx, state); err != nil {
			return state, err
		}
		return l.load(ctx, identity)
	}
	if state.RenewalState == RenewalPending {
		if err := l.reconcileRenewal(ctx, state); err != nil {
			return state, err
		}
		return l.load(ctx, identity)
	}
	return state, nil
}

func (l *Lifecycle) ReconcileAndEnsure(ctx context.Context, identity Identity) (State, error) {
	if _, err := l.Reconcile(ctx, identity); err != nil {
		return State{}, err
	}
	return l.EnsureUsable(ctx, identity)
}

func (l *Lifecycle) material(ctx context.Context, identity Identity) (Material, error) {
	if l == nil || l.Materials == nil {
		return Material{}, ErrMaterialStoreRequired
	}
	material, err := l.Materials.Load(ctx, identity)
	if err != nil {
		return Material{}, err
	}
	return material, nil
}

func (l *Lifecycle) reconcileRenewal(ctx context.Context, state State) error {
	if l == nil || l.Renewal == nil {
		return ErrRenewalProviderRequired
	}
	material, err := l.material(ctx, state.Identity)
	if err != nil {
		return l.failRenewal(ctx, state.Identity, err)
	}
	if err := material.validateForRenewal(); err != nil {
		return l.failRenewal(ctx, state.Identity, err)
	}
	issued, err := l.Renewal.Renew(ctx, RenewalRequest{
		Identity: state.Identity,
		State:    state,
		Material: material,
	})
	if err != nil {
		return l.failRenewal(ctx, state.Identity, err)
	}
	if err := issued.Validate(l.now()); err != nil {
		return l.failRenewal(ctx, state.Identity, err)
	}
	if err := l.Materials.Persist(ctx, state.Identity, issued); err != nil {
		return l.failRenewal(ctx, state.Identity, fmt.Errorf("persist rotated material: %w", err))
	}
	if err := l.Store.RecordIssued(ctx, state.Identity, issued, l.now()); err != nil {
		return l.failRenewal(ctx, state.Identity, fmt.Errorf("persist canonical issued state: %w", err))
	}
	if l.Cache != nil {
		l.Cache.Invalidate(state.Identity)
	}
	return nil
}

func (l *Lifecycle) failRenewal(ctx context.Context, identity Identity, cause error) error {
	if err := l.Store.MarkRenewalFailed(ctx, identity, cause.Error()); err != nil {
		return errors.Join(fmt.Errorf("%w: %v", ErrRenewalFailed, cause), fmt.Errorf("persist renewal failure: %w", err))
	}
	return fmt.Errorf("%w: %v", ErrRenewalFailed, cause)
}

func (l *Lifecycle) reconcileRevocation(ctx context.Context, state State) error {
	if l == nil || l.Revocation == nil {
		return ErrRevocationProviderRequired
	}
	material, err := l.material(ctx, state.Identity)
	if err != nil {
		return l.failRevocation(ctx, state.Identity, err)
	}
	if err := material.validateForRevocation(); err != nil {
		return l.failRevocation(ctx, state.Identity, err)
	}
	if err := l.Revocation.Revoke(ctx, RevocationRequest{
		Identity:          state.Identity,
		State:             state,
		Material:          material,
		CertificateSerial: state.CertificateSerial,
		Reason:            state.RevocationReason,
	}); err != nil {
		return l.failRevocation(ctx, state.Identity, err)
	}
	if err := l.Store.MarkRevoked(ctx, state.Identity, state.RevocationReason, l.now()); err != nil {
		return l.failRevocation(ctx, state.Identity, fmt.Errorf("persist canonical revoked state: %w", err))
	}
	if l.Cache != nil {
		l.Cache.Invalidate(state.Identity)
	}
	return nil
}

func (l *Lifecycle) failRevocation(ctx context.Context, identity Identity, cause error) error {
	if err := l.Store.MarkRevocationFailed(ctx, identity, cause.Error()); err != nil {
		return errors.Join(fmt.Errorf("%w: %v", ErrRevocationFailed, cause), fmt.Errorf("persist revocation failure: %w", err))
	}
	return fmt.Errorf("%w: %v", ErrRevocationFailed, cause)
}
