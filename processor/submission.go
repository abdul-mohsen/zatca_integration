package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zatca-go/zatca/models"
)

// SubmissionState is the provider outcome for one immutable invoice request.
// Pending is only used while an attempt is being finalized; callers must not
// project it as an issued invoice.
type SubmissionState string

const (
	SubmissionPending             SubmissionState = "pending"
	SubmissionAccepted            SubmissionState = "accepted"
	SubmissionAcceptedWithWarning SubmissionState = "accepted_with_warning"
	SubmissionAcceptedWithWarn    SubmissionState = SubmissionAcceptedWithWarning
	SubmissionRejected            SubmissionState = "rejected"
	SubmissionAmbiguous           SubmissionState = "ambiguous"
	SubmissionTerminal            SubmissionState = "terminal"
)

// RetryClass explains what a worker may do after an attempt. In particular,
// RetryReconcile forbids submitting the same invoice again until the provider
// lookup has returned an authoritative "not found" result.
type RetryClass string

const (
	RetryNone         RetryClass = "none"
	RetryReconcile    RetryClass = "reconcile_before_retry"
	RetryBackoff      RetryClass = "bounded_backoff"
	RetryManualReview RetryClass = "manual_review"
)

var (
	ErrSubmissionStoreRequired    = errors.New("submission store is required")
	ErrSubmissionProviderRequired = errors.New("submission provider is required")
	ErrSubmissionAttemptExists    = errors.New("submission attempt already exists")
	ErrSubmissionAttemptNotFound  = errors.New("submission attempt not found")
	ErrSubmissionAttemptFinalized = errors.New("submission attempt is already finalized")
	ErrSubmissionRetryNotDue      = errors.New("submission retry is not due")
)

// SubmissionIdentity is the canonical identity shared by the backend outbox,
// EGS sequence document, and every provider attempt.
type SubmissionIdentity struct {
	TenantID      uint64
	EGSSequenceID string
	BillRequestID string
	EventID       string
	InvoiceUUID   string
	PosID         string
	DocumentType  string
	SourceID      uint64
}

func (i SubmissionIdentity) Validate() error {
	switch {
	case i.TenantID == 0:
		return errors.New("submission tenant_id is required")
	case strings.TrimSpace(i.EGSSequenceID) == "":
		return errors.New("submission egs_sequence_id is required")
	case strings.TrimSpace(i.BillRequestID) == "":
		return errors.New("submission bill_request_id is required")
	case strings.TrimSpace(i.EventID) == "":
		return errors.New("submission event_id is required")
	case strings.TrimSpace(i.InvoiceUUID) == "":
		return errors.New("submission invoice_uuid is required")
	case strings.TrimSpace(i.PosID) == "":
		return errors.New("submission pos_id is required")
	case strings.TrimSpace(i.DocumentType) == "":
		return errors.New("submission document_type is required")
	case i.SourceID == 0:
		return errors.New("submission source_id is required")
	default:
		return nil
	}
}

func (i SubmissionIdentity) key() string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s\x00%d",
		i.TenantID, i.EGSSequenceID, i.BillRequestID, i.InvoiceUUID, i.DocumentType, i.SourceID)
}

// SubmissionRequest contains the already-built, signed provider request. A
// retry must reuse these bytes rather than rebuilding from mutable business
// rows.
type SubmissionRequest struct {
	Identity        SubmissionIdentity
	Standard        bool
	SignedRequest   []byte
	ProviderRequest []byte
	InvoiceHash     string
	QRCode          []byte
}

// ProviderResponse contains normalized provider data and its raw artifact.
// Found is used only by Lookup: false is an authoritative "not found", while
// a nil response means the provider outcome is still unknown.
type ProviderResponse struct {
	Found            bool
	HTTPStatus       int
	Status           string
	InvoiceHash      string
	ResponseArtifact []byte
	ClearedArtifact  []byte
	Warnings         []models.ErrorDetail
	Errors           []models.ErrorDetail
}

// ProviderHTTPError preserves a non-success HTTP body for the immutable
// attempt artifact and lets the retry classifier inspect the status.
type ProviderHTTPError struct {
	StatusCode int
	Body       []byte
	Err        error
}

func (e *ProviderHTTPError) Error() string {
	if e == nil {
		return "provider HTTP error"
	}
	if e.Err != nil {
		return fmt.Sprintf("provider HTTP %d: %v", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("provider HTTP %d", e.StatusCode)
}

func (e *ProviderHTTPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// SubmissionProvider is the narrow provider boundary used by the reliability
// engine. Lookup is mandatory so an ambiguous response cannot be blindly
// resubmitted.
type SubmissionProvider interface {
	Submit(context.Context, SubmissionRequest) (*ProviderResponse, error)
	Lookup(context.Context, SubmissionIdentity) (*ProviderResponse, error)
}

// SubmissionAttempt is append-only identity and artifact data for one submit
// or reconciliation operation. Request artifacts are never replaced.
type SubmissionAttempt struct {
	ID                 string
	Identity           SubmissionIdentity
	Attempt            int
	Standard           bool
	Reconciliation     bool
	SignedRequest      []byte
	RequestArtifact    []byte
	ResponseArtifact   []byte
	ClearedArtifact    []byte
	InvoiceHash        string
	State              SubmissionState
	Retry              RetryClass
	NextAttemptAt      time.Time
	DeadLetter         bool
	ManualReview       bool
	ProviderHTTPStatus int
	ErrorMessage       string
	CreatedAt          time.Time
	FinalizedAt        time.Time
}

func (a SubmissionAttempt) successful() bool {
	return a.State == SubmissionAccepted || a.State == SubmissionAcceptedWithWarn
}

func (a SubmissionAttempt) outcomeError() error {
	if a.successful() {
		return nil
	}
	message := a.ErrorMessage
	if message == "" {
		message = string(a.State)
	}
	return &SubmissionOutcomeError{
		State:         a.State,
		Retry:         a.Retry,
		NextAttemptAt: a.NextAttemptAt,
		DeadLetter:    a.DeadLetter,
		ManualReview:  a.ManualReview,
		Err:           errors.New(message),
	}
}

// SubmissionResult is returned for both first delivery and duplicate delivery.
type SubmissionResult struct {
	Attempt    SubmissionAttempt
	Duplicate  bool
	Reconciled bool
}

// SubmissionOutcomeError carries durable retry/manual-review state to callers.
type SubmissionOutcomeError struct {
	State         SubmissionState
	Retry         RetryClass
	NextAttemptAt time.Time
	DeadLetter    bool
	ManualReview  bool
	Err           error
}

func (e *SubmissionOutcomeError) Error() string {
	if e == nil {
		return "submission outcome error"
	}
	message := "submission outcome"
	if e.Err != nil {
		message = e.Err.Error()
	}
	if e.NextAttemptAt.IsZero() {
		return fmt.Sprintf("%s: state=%s retry=%s", message, e.State, e.Retry)
	}
	return fmt.Sprintf("%s: state=%s retry=%s next_attempt_at=%s", message, e.State, e.Retry, e.NextAttemptAt.UTC().Format(time.RFC3339))
}

func (e *SubmissionOutcomeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// SubmissionStore durably records an attempt before the provider call and
// finalizes only its outcome fields afterwards. Implementations must preserve
// all request artifacts byte-for-byte.
type SubmissionStore interface {
	Latest(context.Context, SubmissionIdentity) (SubmissionAttempt, bool, error)
	Begin(context.Context, SubmissionAttempt) (SubmissionAttempt, error)
	Finalize(context.Context, SubmissionAttempt) (SubmissionAttempt, error)
}

// SubmissionEngine applies provider outcome, reconciliation, and retry rules.
type SubmissionEngine struct {
	Provider    SubmissionProvider
	Store       SubmissionStore
	Now         func() time.Time
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	MaxAttempts int
}

func NewSubmissionEngine(provider SubmissionProvider, store SubmissionStore) *SubmissionEngine {
	return &SubmissionEngine{
		Provider:    provider,
		Store:       store,
		BaseBackoff: time.Second,
		MaxBackoff:  5 * time.Minute,
		MaxAttempts: 5,
	}
}

func (e *SubmissionEngine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *SubmissionEngine) Submit(ctx context.Context, request SubmissionRequest) (SubmissionResult, error) {
	if err := request.Identity.Validate(); err != nil {
		return SubmissionResult{}, err
	}
	if e == nil || e.Provider == nil {
		return SubmissionResult{}, ErrSubmissionProviderRequired
	}
	if e.Store == nil {
		return SubmissionResult{}, ErrSubmissionStoreRequired
	}

	latest, found, err := e.Store.Latest(ctx, request.Identity)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("load latest submission attempt: %w", err)
	}
	if found {
		request = requestFromAttempt(request, latest)
		if latest.successful() {
			return SubmissionResult{Attempt: latest, Duplicate: true}, nil
		}
		if latest.State == SubmissionRejected || latest.State == SubmissionTerminal {
			return SubmissionResult{Attempt: latest, Duplicate: true}, latest.outcomeError()
		}
		if latest.DeadLetter || latest.ManualReview {
			return SubmissionResult{Attempt: latest, Duplicate: true}, latest.outcomeError()
		}
		now := e.now()
		if !latest.NextAttemptAt.IsZero() && now.Before(latest.NextAttemptAt) {
			return SubmissionResult{Attempt: latest}, &SubmissionOutcomeError{
				State:         latest.State,
				Retry:         latest.Retry,
				NextAttemptAt: latest.NextAttemptAt,
				DeadLetter:    latest.DeadLetter,
				ManualReview:  latest.ManualReview,
				Err:           ErrSubmissionRetryNotDue,
			}
		}

		reconciled, lookupResult, lookupErr := e.reconcile(ctx, request, latest)
		if lookupErr != nil {
			return reconciled, lookupErr
		}
		if lookupResult.Attempt.State != SubmissionAmbiguous {
			if lookupResult.Attempt.successful() {
				lookupResult.Reconciled = true
				return lookupResult, nil
			}
			lookupResult.Reconciled = true
			return lookupResult, lookupResult.Attempt.outcomeError()
		}
		if !lookupResult.Attempt.ResponseArtifactIsNotFound() {
			return lookupResult, lookupResult.Attempt.outcomeError()
		}
		// The provider explicitly confirmed that the request is not known.
		// Only now is another submission permitted.
		request = requestFromAttempt(request, latest)
		return e.submit(ctx, request, latest.Attempt+2)
	}

	if len(request.SignedRequest) == 0 || len(request.ProviderRequest) == 0 {
		return SubmissionResult{}, errors.New("signed and provider request artifacts are required")
	}
	return e.submit(ctx, request, 1)
}

func (e *SubmissionEngine) submit(ctx context.Context, request SubmissionRequest, attemptNumber int) (SubmissionResult, error) {
	if e.MaxAttempts > 0 && attemptNumber > e.MaxAttempts {
		return e.persistManualReview(ctx, request, attemptNumber, false)
	}

	start := SubmissionAttempt{
		Identity:        request.Identity,
		Attempt:         attemptNumber,
		Standard:        request.Standard,
		SignedRequest:   cloneBytes(request.SignedRequest),
		RequestArtifact: cloneBytes(request.ProviderRequest),
		InvoiceHash:     request.InvoiceHash,
		State:           SubmissionPending,
		Retry:           RetryNone,
		CreatedAt:       e.now(),
	}
	started, err := e.Store.Begin(ctx, start)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("persist submission request artifact: %w", err)
	}

	response, providerErr := e.Provider.Submit(ctx, request)
	state, retry, status, responseArtifact, clearedArtifact, message := classifyProviderResult(response, providerErr)
	finished := started
	finished.State = state
	finished.Retry = retry
	finished.ProviderHTTPStatus = status
	finished.ResponseArtifact = cloneBytes(responseArtifact)
	finished.ClearedArtifact = cloneBytes(clearedArtifact)
	finished.ErrorMessage = message
	if response != nil {
		if response.InvoiceHash != "" {
			finished.InvoiceHash = response.InvoiceHash
		}
	}
	if state == SubmissionAmbiguous && retry == RetryReconcile {
		finished.NextAttemptAt = e.nextAttemptAt(attemptNumber)
	}
	if state == SubmissionAmbiguous && e.MaxAttempts > 0 && attemptNumber >= e.MaxAttempts {
		finished.DeadLetter = true
		finished.ManualReview = true
		finished.Retry = RetryManualReview
		finished.NextAttemptAt = time.Time{}
	}
	if state == SubmissionTerminal {
		finished.ManualReview = true
	}
	finished.FinalizedAt = e.now()
	finalized, err := e.Store.Finalize(ctx, finished)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("persist submission outcome: %w", err)
	}
	result := SubmissionResult{Attempt: finalized}
	if finalized.successful() {
		return result, nil
	}
	return result, finalized.outcomeError()
}

func (e *SubmissionEngine) persistManualReview(
	ctx context.Context,
	request SubmissionRequest,
	attemptNumber int,
	reconciliation bool,
) (SubmissionResult, error) {
	attempt := SubmissionAttempt{
		Identity:        request.Identity,
		Attempt:         attemptNumber,
		Standard:        request.Standard,
		Reconciliation:  reconciliation,
		SignedRequest:   cloneBytes(request.SignedRequest),
		RequestArtifact: cloneBytes(request.ProviderRequest),
		InvoiceHash:     request.InvoiceHash,
		State:           SubmissionAmbiguous,
		Retry:           RetryManualReview,
		DeadLetter:      true,
		ManualReview:    true,
		ErrorMessage:    "maximum provider attempts exhausted",
		CreatedAt:       e.now(),
	}
	started, err := e.Store.Begin(ctx, attempt)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("persist dead-letter request artifact: %w", err)
	}
	finished := started
	finished.FinalizedAt = e.now()
	finalized, err := e.Store.Finalize(ctx, finished)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("persist dead-letter outcome: %w", err)
	}
	result := SubmissionResult{Attempt: finalized}
	return result, finalized.outcomeError()
}

func (e *SubmissionEngine) reconcile(
	ctx context.Context,
	request SubmissionRequest,
	latest SubmissionAttempt,
) (SubmissionResult, SubmissionResult, error) {
	if e.MaxAttempts > 0 && latest.Attempt+1 > e.MaxAttempts {
		result, err := e.persistManualReview(ctx, request, latest.Attempt+1, true)
		return result, result, err
	}
	start := SubmissionAttempt{
		Identity:        latest.Identity,
		Attempt:         latest.Attempt + 1,
		Standard:        latest.Standard,
		Reconciliation:  true,
		SignedRequest:   cloneBytes(latest.SignedRequest),
		RequestArtifact: cloneBytes(latest.RequestArtifact),
		InvoiceHash:     latest.InvoiceHash,
		State:           SubmissionPending,
		Retry:           RetryReconcile,
		CreatedAt:       e.now(),
	}
	started, err := e.Store.Begin(ctx, start)
	if err != nil {
		return SubmissionResult{}, SubmissionResult{}, fmt.Errorf("persist reconciliation request artifact: %w", err)
	}
	response, lookupErr := e.Provider.Lookup(ctx, request.Identity)
	if lookupErr != nil || response == nil {
		finished := started
		finished.State = SubmissionAmbiguous
		finished.Retry = RetryReconcile
		finished.NextAttemptAt = e.nextAttemptAt(start.Attempt)
		finished.ManualReview = true
		if lookupErr != nil {
			finished.ErrorMessage = fmt.Sprintf("provider reconciliation failed: %v", lookupErr)
		} else {
			finished.ErrorMessage = "provider reconciliation returned nil response"
		}
		if e.MaxAttempts > 0 && start.Attempt >= e.MaxAttempts {
			finished.DeadLetter = true
			finished.Retry = RetryManualReview
			finished.NextAttemptAt = time.Time{}
		}
		finished.FinalizedAt = e.now()
		finalized, finalizeErr := e.Store.Finalize(ctx, finished)
		if finalizeErr != nil {
			return SubmissionResult{}, SubmissionResult{}, fmt.Errorf("persist reconciliation outcome: %w", finalizeErr)
		}
		result := SubmissionResult{Attempt: finalized, Reconciled: true}
		return result, result, finalized.outcomeError()
	}

	if !response.Found {
		finished := started
		finished.State = SubmissionAmbiguous
		finished.Retry = RetryNone
		finished.ResponseArtifact = cloneBytes(response.ResponseArtifact)
		finished.ErrorMessage = "provider reconciliation confirmed request was not found"
		finished.FinalizedAt = e.now()
		finalized, finalizeErr := e.Store.Finalize(ctx, finished)
		if finalizeErr != nil {
			return SubmissionResult{}, SubmissionResult{}, fmt.Errorf("persist not-found reconciliation: %w", finalizeErr)
		}
		return SubmissionResult{Attempt: finalized, Reconciled: true},
			SubmissionResult{Attempt: finalized, Reconciled: true}, nil
	}

	state, retry, status, responseArtifact, clearedArtifact, message := classifyProviderResult(response, nil)
	finished := started
	finished.State = state
	finished.Retry = retry
	finished.ProviderHTTPStatus = status
	finished.ResponseArtifact = cloneBytes(responseArtifact)
	finished.ClearedArtifact = cloneBytes(clearedArtifact)
	finished.ErrorMessage = message
	if response.InvoiceHash != "" {
		finished.InvoiceHash = response.InvoiceHash
	}
	if state == SubmissionAmbiguous && retry == RetryReconcile {
		finished.NextAttemptAt = e.nextAttemptAt(start.Attempt)
	}
	if state == SubmissionTerminal {
		finished.ManualReview = true
	}
	finished.FinalizedAt = e.now()
	finalized, finalizeErr := e.Store.Finalize(ctx, finished)
	if finalizeErr != nil {
		return SubmissionResult{}, SubmissionResult{}, fmt.Errorf("persist reconciliation outcome: %w", finalizeErr)
	}
	result := SubmissionResult{Attempt: finalized, Reconciled: true}
	return result, result, finalized.outcomeError()
}

func requestFromAttempt(request SubmissionRequest, attempt SubmissionAttempt) SubmissionRequest {
	request.Standard = attempt.Standard
	request.SignedRequest = cloneBytes(attempt.SignedRequest)
	request.ProviderRequest = cloneBytes(attempt.RequestArtifact)
	request.InvoiceHash = attempt.InvoiceHash
	request.QRCode = nil
	return request
}

func (e *SubmissionEngine) nextAttemptAt(attempt int) time.Time {
	backoff := e.BaseBackoff
	if backoff <= 0 {
		return time.Time{}
	}
	if attempt < 1 {
		attempt = 1
	}
	for n := 1; n < attempt; n++ {
		if backoff > time.Duration(1<<62) || backoff > e.MaxBackoff/2 && e.MaxBackoff > 0 {
			backoff = e.MaxBackoff
			break
		}
		backoff *= 2
	}
	if e.MaxBackoff > 0 && backoff > e.MaxBackoff {
		backoff = e.MaxBackoff
	}
	return e.now().Add(backoff)
}

func classifyProviderResult(response *ProviderResponse, providerErr error) (
	SubmissionState,
	RetryClass,
	int,
	[]byte,
	[]byte,
	string,
) {
	if providerErr != nil {
		var httpErr *ProviderHTTPError
		if errors.As(providerErr, &httpErr) {
			switch {
			case httpErr.StatusCode == http.StatusRequestTimeout ||
				httpErr.StatusCode == http.StatusTooManyRequests ||
				httpErr.StatusCode >= 500:
				return SubmissionAmbiguous, RetryReconcile, httpErr.StatusCode, cloneBytes(httpErr.Body), nil, providerErr.Error()
			case httpErr.StatusCode >= 400 && httpErr.StatusCode < 500:
				return SubmissionRejected, RetryNone, httpErr.StatusCode, cloneBytes(httpErr.Body), nil, providerErr.Error()
			default:
				return SubmissionTerminal, RetryNone, httpErr.StatusCode, cloneBytes(httpErr.Body), nil, providerErr.Error()
			}
		}
		if isTimeoutError(providerErr) {
			return SubmissionAmbiguous, RetryReconcile, 0, nil, nil, providerErr.Error()
		}
		return SubmissionTerminal, RetryNone, 0, nil, nil, providerErr.Error()
	}
	if response == nil {
		return SubmissionAmbiguous, RetryReconcile, 0, nil, nil, "provider returned nil response"
	}

	status := response.HTTPStatus
	normalized := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(response.Status), "-", "_"), " ", "_"))
	responseArtifact := cloneBytes(response.ResponseArtifact)
	clearedArtifact := cloneBytes(response.ClearedArtifact)
	if len(response.Errors) > 0 {
		return SubmissionRejected, RetryNone, status, responseArtifact, clearedArtifact, "provider rejected invoice"
	}
	if strings.Contains(normalized, "REJECT") || strings.Contains(normalized, "INVALID") ||
		strings.Contains(normalized, "FAIL") || strings.Contains(normalized, "ERROR") {
		return SubmissionRejected, RetryNone, status, responseArtifact, clearedArtifact, "provider rejected invoice: " + response.Status
	}
	if strings.Contains(normalized, "WARNING") || len(response.Warnings) > 0 {
		return SubmissionAcceptedWithWarn, RetryNone, status, responseArtifact, clearedArtifact, "provider accepted invoice with warning"
	}
	if normalized == "ACCEPTED" || normalized == "CLEARED" || normalized == "REPORTED" ||
		normalized == "SUCCESS" || normalized == "PASS" || normalized == "REPORTED_SUCCESS" {
		return SubmissionAccepted, RetryNone, status, responseArtifact, clearedArtifact, ""
	}
	return SubmissionTerminal, RetryNone, status, responseArtifact, clearedArtifact, "provider returned an unrecognized outcome"
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

// ResponseArtifactIsNotFound marks the special reconciliation result that
// permits a new submit. It is intentionally encoded as a response marker so
// the store can preserve the lookup result without adding a mutable flag.
func (a SubmissionAttempt) ResponseArtifactIsNotFound() bool {
	return a.Reconciliation && a.State == SubmissionAmbiguous &&
		a.Retry == RetryNone &&
		strings.Contains(a.ErrorMessage, "not found")
}

// MemorySubmissionStore is used by provider fakes and deterministic tests.
// It copies every artifact on write and read to make accidental mutation
// visible rather than silently changing the audit trail.
type MemorySubmissionStore struct {
	mu       sync.Mutex
	attempts map[string][]SubmissionAttempt
	Now      func() time.Time
}

func NewMemorySubmissionStore() *MemorySubmissionStore {
	return &MemorySubmissionStore{attempts: make(map[string][]SubmissionAttempt)}
}

func (s *MemorySubmissionStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *MemorySubmissionStore) Latest(_ context.Context, identity SubmissionIdentity) (SubmissionAttempt, bool, error) {
	if err := identity.Validate(); err != nil {
		return SubmissionAttempt{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts := s.attempts[identity.key()]
	if len(attempts) == 0 {
		return SubmissionAttempt{}, false, nil
	}
	return cloneAttempt(attempts[len(attempts)-1]), true, nil
}

func (s *MemorySubmissionStore) Begin(_ context.Context, attempt SubmissionAttempt) (SubmissionAttempt, error) {
	if err := attempt.Identity.Validate(); err != nil {
		return SubmissionAttempt{}, err
	}
	if len(attempt.SignedRequest) == 0 || len(attempt.RequestArtifact) == 0 {
		return SubmissionAttempt{}, errors.New("submission artifacts are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts == nil {
		s.attempts = make(map[string][]SubmissionAttempt)
	}
	key := attempt.Identity.key()
	if attempt.Attempt <= 0 {
		attempt.Attempt = len(s.attempts[key]) + 1
	}
	for _, existing := range s.attempts[key] {
		if existing.Attempt == attempt.Attempt {
			return SubmissionAttempt{}, ErrSubmissionAttemptExists
		}
	}
	if attempt.ID == "" {
		attempt.ID = fmt.Sprintf("%s:%d", key, attempt.Attempt)
	}
	if attempt.CreatedAt.IsZero() {
		attempt.CreatedAt = s.now()
	}
	attempt.SignedRequest = cloneBytes(attempt.SignedRequest)
	attempt.RequestArtifact = cloneBytes(attempt.RequestArtifact)
	s.attempts[key] = append(s.attempts[key], cloneAttempt(attempt))
	return cloneAttempt(attempt), nil
}

func (s *MemorySubmissionStore) Finalize(_ context.Context, attempt SubmissionAttempt) (SubmissionAttempt, error) {
	if err := attempt.Identity.Validate(); err != nil {
		return SubmissionAttempt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := attempt.Identity.key()
	items := s.attempts[key]
	for index := range items {
		if items[index].ID != attempt.ID {
			continue
		}
		if !items[index].FinalizedAt.IsZero() {
			return SubmissionAttempt{}, ErrSubmissionAttemptFinalized
		}
		original := items[index]
		original.ResponseArtifact = cloneBytes(attempt.ResponseArtifact)
		original.ClearedArtifact = cloneBytes(attempt.ClearedArtifact)
		original.InvoiceHash = attempt.InvoiceHash
		original.State = attempt.State
		original.Retry = attempt.Retry
		original.NextAttemptAt = attempt.NextAttemptAt
		original.DeadLetter = attempt.DeadLetter
		original.ManualReview = attempt.ManualReview
		original.ProviderHTTPStatus = attempt.ProviderHTTPStatus
		original.ErrorMessage = attempt.ErrorMessage
		original.FinalizedAt = attempt.FinalizedAt
		if original.FinalizedAt.IsZero() {
			original.FinalizedAt = s.now()
		}
		items[index] = cloneAttempt(original)
		return cloneAttempt(original), nil
	}
	return SubmissionAttempt{}, ErrSubmissionAttemptNotFound
}

func (s *MemorySubmissionStore) Attempts(identity SubmissionIdentity) []SubmissionAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.attempts[identity.key()]
	result := make([]SubmissionAttempt, len(items))
	for index := range items {
		result[index] = cloneAttempt(items[index])
	}
	return result
}

func cloneAttempt(attempt SubmissionAttempt) SubmissionAttempt {
	attempt.SignedRequest = cloneBytes(attempt.SignedRequest)
	attempt.RequestArtifact = cloneBytes(attempt.RequestArtifact)
	attempt.ResponseArtifact = cloneBytes(attempt.ResponseArtifact)
	attempt.ClearedArtifact = cloneBytes(attempt.ClearedArtifact)
	return attempt
}

// SQLSubmissionStore persists append-only attempts and the backend's generic
// zatca_submission projection. The attempt table is the authoritative audit
// trail; the projection is updated only after an attempt is finalized.
type SQLSubmissionStore struct {
	DB  *sql.DB
	Now func() time.Time
}

func NewSQLSubmissionStore(db *sql.DB) *SQLSubmissionStore {
	return &SQLSubmissionStore{DB: db}
}

func (s *SQLSubmissionStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *SQLSubmissionStore) Latest(ctx context.Context, identity SubmissionIdentity) (SubmissionAttempt, bool, error) {
	if err := identity.Validate(); err != nil {
		return SubmissionAttempt{}, false, err
	}
	if s == nil || s.DB == nil {
		return SubmissionAttempt{}, false, ErrSubmissionStoreRequired
	}
	var attempt SubmissionAttempt
	var (
		posID, documentType, state, retry, signedRequest, requestArtifact                            string
		responseArtifact, clearedArtifact, invoiceHash, errorMessage                                 sql.NullString
		attemptID, attemptNumber, sourceID, providerStatus, deadLetter, manualReview, reconciliation sql.NullInt64
		nextAttempt, createdAt, finalizedAt                                                          sql.NullTime
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, pos_id, document_type, source_id, attempt_no, reconciliation,
		       signed_request_xml, request_payload, response_payload, cleared_xml,
		       invoice_hash, state, retry_class, next_attempt_at,
		       dead_lettered, manual_review_required, provider_http_status,
		       error_message, created_at, finalized_at
		FROM zatca_submission_attempt
		WHERE tenant_id = ? AND egs_sequence_id = ?
		  AND bill_request_id = ? AND invoice_uuid = ?
		ORDER BY attempt_no DESC, id DESC
		LIMIT 1
	`, identity.TenantID, identity.EGSSequenceID, identity.BillRequestID, identity.InvoiceUUID).Scan(
		&attemptID, &posID, &documentType, &sourceID, &attemptNumber, &reconciliation,
		&signedRequest, &requestArtifact, &responseArtifact, &clearedArtifact,
		&invoiceHash, &state, &retry, &nextAttempt,
		&deadLetter, &manualReview, &providerStatus, &errorMessage,
		&createdAt, &finalizedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SubmissionAttempt{}, false, nil
	}
	if err != nil {
		return SubmissionAttempt{}, false, fmt.Errorf("load submission attempt: %w", err)
	}
	attempt.ID = fmt.Sprintf("%d", attemptID.Int64)
	attempt.Identity = identity
	attempt.Identity.PosID = posID
	attempt.Identity.DocumentType = documentType
	attempt.Identity.SourceID = uint64(sourceID.Int64)
	attempt.Attempt = int(attemptNumber.Int64)
	attempt.Reconciliation = reconciliation.Int64 != 0
	attempt.SignedRequest = []byte(signedRequest)
	attempt.RequestArtifact = []byte(requestArtifact)
	attempt.ResponseArtifact = []byte(responseArtifact.String)
	attempt.ClearedArtifact = []byte(clearedArtifact.String)
	attempt.InvoiceHash = invoiceHash.String
	attempt.State = SubmissionState(state)
	attempt.Retry = RetryClass(retry)
	if nextAttempt.Valid {
		attempt.NextAttemptAt = nextAttempt.Time
	}
	attempt.DeadLetter = deadLetter.Int64 != 0
	attempt.ManualReview = manualReview.Int64 != 0
	attempt.ProviderHTTPStatus = int(providerStatus.Int64)
	attempt.ErrorMessage = errorMessage.String
	if createdAt.Valid {
		attempt.CreatedAt = createdAt.Time
	}
	if finalizedAt.Valid {
		attempt.FinalizedAt = finalizedAt.Time
	}
	return attempt, true, nil
}

func (s *SQLSubmissionStore) Begin(ctx context.Context, attempt SubmissionAttempt) (SubmissionAttempt, error) {
	if err := attempt.Identity.Validate(); err != nil {
		return SubmissionAttempt{}, err
	}
	if s == nil || s.DB == nil {
		return SubmissionAttempt{}, ErrSubmissionStoreRequired
	}
	if len(attempt.SignedRequest) == 0 || len(attempt.RequestArtifact) == 0 {
		return SubmissionAttempt{}, errors.New("submission artifacts are required")
	}
	if attempt.CreatedAt.IsZero() {
		attempt.CreatedAt = s.now()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SubmissionAttempt{}, fmt.Errorf("begin submission attempt: %w", err)
	}
	defer tx.Rollback()
	if attempt.State == "" {
		attempt.State = SubmissionPending
	}
	if attempt.Retry == "" {
		attempt.Retry = RetryNone
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO zatca_submission_attempt (
			tenant_id, egs_sequence_id, bill_request_id, event_id, pos_id,
			document_type, source_id, invoice_uuid, attempt_no, reconciliation,
			signed_request_xml, request_payload, invoice_hash, state, retry_class,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, attempt.Identity.TenantID, attempt.Identity.EGSSequenceID, attempt.Identity.BillRequestID,
		attempt.Identity.EventID, attempt.Identity.PosID, attempt.Identity.DocumentType,
		attempt.Identity.SourceID, attempt.Identity.InvoiceUUID, attempt.Attempt,
		attempt.Reconciliation, string(attempt.SignedRequest), string(attempt.RequestArtifact),
		attempt.InvoiceHash, string(attempt.State), string(attempt.Retry), attempt.CreatedAt)
	if err != nil {
		return SubmissionAttempt{}, fmt.Errorf("insert submission attempt: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return SubmissionAttempt{}, fmt.Errorf("read submission attempt id: %w", err)
	}
	attempt.ID = fmt.Sprintf("%d", id)
	if err := upsertSubmissionProjection(ctx, tx, attempt); err != nil {
		return SubmissionAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return SubmissionAttempt{}, fmt.Errorf("commit submission attempt: %w", err)
	}
	return cloneAttempt(attempt), nil
}

func (s *SQLSubmissionStore) Finalize(ctx context.Context, attempt SubmissionAttempt) (SubmissionAttempt, error) {
	if err := attempt.Identity.Validate(); err != nil {
		return SubmissionAttempt{}, err
	}
	if s == nil || s.DB == nil {
		return SubmissionAttempt{}, ErrSubmissionStoreRequired
	}
	if attempt.FinalizedAt.IsZero() {
		attempt.FinalizedAt = s.now()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SubmissionAttempt{}, fmt.Errorf("begin submission finalization: %w", err)
	}
	defer tx.Rollback()
	var attemptID any = attempt.ID
	if numericID, parseErr := parseAttemptID(attempt.ID); parseErr == nil {
		attemptID = numericID
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE zatca_submission_attempt
		SET response_payload = ?, cleared_xml = ?, invoice_hash = ?,
		    state = ?, retry_class = ?, next_attempt_at = ?,
		    dead_lettered = ?, manual_review_required = ?,
		    provider_http_status = ?, error_message = ?, finalized_at = ?
		WHERE id = ? AND finalized_at IS NULL
	`, string(attempt.ResponseArtifact), string(attempt.ClearedArtifact), attempt.InvoiceHash,
		string(attempt.State), string(attempt.Retry), nullTime(attempt.NextAttemptAt),
		attempt.DeadLetter, attempt.ManualReview, attempt.ProviderHTTPStatus,
		attempt.ErrorMessage, attempt.FinalizedAt, attemptID)
	if err != nil {
		return SubmissionAttempt{}, fmt.Errorf("finalize submission attempt: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return SubmissionAttempt{}, fmt.Errorf("inspect submission finalization: %w", err)
	}
	if affected != 1 {
		return SubmissionAttempt{}, ErrSubmissionAttemptFinalized
	}
	if err := upsertSubmissionProjection(ctx, tx, attempt); err != nil {
		return SubmissionAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return SubmissionAttempt{}, fmt.Errorf("commit submission finalization: %w", err)
	}
	return cloneAttempt(attempt), nil
}

func upsertSubmissionProjection(ctx context.Context, tx *sql.Tx, attempt SubmissionAttempt) error {
	status := 1 // submitted/unknown
	switch attempt.State {
	case SubmissionPending:
		status = 0
	case SubmissionAccepted:
		status = 2
	case SubmissionAcceptedWithWarn:
		status = 4
	case SubmissionRejected, SubmissionTerminal:
		status = 3
	}
	response := attempt.ResponseArtifact
	if len(attempt.ClearedArtifact) > 0 {
		response = attempt.ClearedArtifact
	}
	var submittedAt, clearedAt any
	if !attempt.CreatedAt.IsZero() {
		submittedAt = attempt.CreatedAt
	}
	if attempt.successful() && !attempt.FinalizedAt.IsZero() {
		clearedAt = attempt.FinalizedAt
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO zatca_submission (
			tenant_id, document_type, source_id, pos_id, request_id,
			invoice_uuid, status, rejection_msg, submitted_at, cleared_at,
			response_xml, retry_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			status = VALUES(status),
			rejection_msg = VALUES(rejection_msg),
			submitted_at = VALUES(submitted_at),
			cleared_at = VALUES(cleared_at),
			response_xml = VALUES(response_xml),
			retry_count = VALUES(retry_count),
			updated_at = CURRENT_TIMESTAMP
	`, attempt.Identity.TenantID, attempt.Identity.DocumentType, attempt.Identity.SourceID,
		attempt.Identity.PosID, attempt.Identity.BillRequestID, attempt.Identity.InvoiceUUID,
		status, nullableString(attempt.ErrorMessage), submittedAt, clearedAt,
		nullableString(string(response)), attempt.Attempt)
	if err != nil {
		return fmt.Errorf("upsert submission projection: %w", err)
	}
	return nil
}

func parseAttemptID(value string) (int64, error) {
	var id int64
	_, err := fmt.Sscan(value, &id)
	return id, err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
