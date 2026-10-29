# T020 Research

## Repository Evidence

### Existing retry and acknowledgement path

- `cmd/daemon/worker.go` currently runs every `processMessage` error through
  the same ten-attempt exponential loop, then alerts and ACKs the message.
- The worker has no error-classification branch; a durable `rejected`,
  `ambiguous`, or `terminal_failure` processor result is not enough to stop
  the in-memory retry loop.
- `cmd/daemon/email.go` describes all failures as exhausted retries and does
  not distinguish permanent/dead-letter handling.
- `queue.Message` already carries additive `attempt`, `attempt_count`, and
  `attempt_id` fields from T015, so no new queue contract is needed for the
  T020 retry count.

### Existing provider/client seam

- `processor.SubmissionProvider` is the established signing, request-building,
  QR, submit, and optional `SubmissionReconciler` boundary.
- `client.APIError` preserves HTTP status and response body and is wrapped by
  the reporting/clearance client methods; `errors.As` can recover it.
- `processor.classifyProviderError` currently maps ordinary 4xx responses to
  `OutcomeRejected` but maps 408, 409, 429, and all 5xx to
  `OutcomeAmbiguous`. It does not expose a worker retry/dead-letter decision.
- SDK/XML/request construction failures are returned as ordinary wrapped
  errors from the shared submit path. Invalid XML/content therefore needs a
  stable processor error classification before the worker can avoid retrying
  it.
- The production `serviceSubmissionProvider.Reconcile` already returns
  `RECONCILIATION_UNSUPPORTED` with `OutcomeAmbiguous`, explicitly documenting
  that the current ZATCA client has no lookup-by-UUID endpoint.

### Existing durable contract

- `submissionOutcome` already carries `RetryCount`, `NextRetryAt`,
  `ProviderCalled`, provider/rejection/error fields, and the T003 outcome
  state.
- `sqlSubmissionStore.CompleteSubmission` persists the current submission,
  attempt, append-only outcome, artifacts, and legacy metadata in one
  transaction. `outcomeRetryTime` already calculates bounded exponential
  retry timestamps.
- The T003 state vocabulary has no separate `dead_letter` value or boolean.
  `OutcomeTerminalFailure` plus terminal error code/message and a nil
  `next_retry_at` is the compatible durable representation of a dead-letter
  decision.
- T015's durable gate returns an ambiguous attempt for reconciliation and
  only allocates a new attempt after a definitive `(nil, nil)` provider
  lookup. The processor already avoids a blind resubmission when lookup is
  unsupported or itself ambiguous.

### Existing focused tests

- `processor/outcome_test.go` covers accepted, warning, rejected, timeout,
  pending, and nil outcomes with a fake provider/store.
- `processor/idempotency_test.go` covers accepted duplicate delivery,
  timeout-after-acceptance reconciliation, and not-found identity reuse.
- `client/client_test.go` uses `httptest` and already verifies `APIError`.
- `cmd/daemon/worker_test.go` verifies message context copying but has no fake
  worker retry decision tests.
- Baseline focused commands at `d7bbc1a` pass individually:
  `go test ./processor`, `go test ./cmd/daemon`, and `go test ./client`.

## Decisions

### D1: Keep durable state vocabulary unchanged

Use `OutcomeTerminalFailure` for permanent validation/invalid-XML failures and
exhausted retry dead letters. Preserve `OutcomeRejected` for provider business
rejections where the provider supplied a definitive rejection result, while
marking the worker decision non-retryable. This avoids a migration and keeps
T003 monitor compatibility.

### D2: Separate retry decision from outcome state

Add a small processor-level classification helper/metadata to make the
worker decision explicit rather than inferring retryability from every error
string. The durable outcome remains the source of truth; the worker uses the
typed error to decide whether to retry, dead-letter, or stop after success.

### D3: Conservative status policy

Treat 408, 409, 425, 429, and 5xx as retryable/ambiguous because a provider
may have accepted the request or may be temporarily throttling/concurrent.
Treat other 4xx as permanent. Treat timeout and transport errors as
retryable/ambiguous. Treat malformed XML, malformed provider payloads, and
pre-provider request/signing failures as terminal.

### D4: Reuse the existing reconciliation gate

Do not redesign T015. Ensure T020's transient worker retry path re-enters the
existing processor gate, so an ambiguous prior submission calls
`SubmissionReconciler` before any new `Submit`. A positive result is persisted
without another submit; the current production adapter continues to report
the lookup limitation explicitly.

### D5: Test worker policy through an injectable attempt function

Preserve `processWithRetry`'s runtime signature and factor the loop's decision
logic into a small injectable helper for deterministic tests. Tests can then
prove permanent errors perform one attempt and transient errors consume the
bounded policy without requiring NATS, MySQL, SDK Docker, or real provider
credentials.

## Alternatives Considered

1. **Retry every error and rely only on T003 state**: rejected because the
   worker would still resubmit permanent invalid work and could exhaust the
   queue budget before operators see it.
2. **Add a new durable `dead_letter` database state**: rejected because the
   T003 state contract is fixed and a migration is out of scope. Terminal
   failure plus error/dead-letter metadata is the compatible representation.
3. **Blindly resubmit after a timeout**: rejected by T015; provider acceptance
   is ambiguous and a duplicate invoice is unsafe.
4. **Probe the live provider for lookup during tests**: rejected because no
   current client endpoint exists and live credentials are unavailable.

## Remaining Limitation

The production ZATCA client exposes report/clear submission methods but no
lookup-by-UUID/invoice-hash endpoint. Therefore timeout-after-acceptance
reconciliation is fully exercised with a fake provider, while the production
adapter safely returns `RECONCILIATION_UNSUPPORTED` and leaves the outcome
ambiguous rather than issuing a duplicate request.
