# Requirements: ZATCA Retry Classification and Reconciliation

**Feature:** T020 — ZATCA retry classification and timeout reconciliation  
**Base:** daemon commit `d7bbc1a` (T004 outcome persistence + T015 idempotency)

## Problem

The daemon currently treats every processing error as retryable. Permanent
provider rejections and invalid invoice/XML conditions therefore consume the
same retry budget as transient transport failures, while terminal work is
acknowledged without a durable dead-letter/terminal record. A timeout can also
leave ZATCA having accepted an invoice even though the daemon only sees an
error; resubmitting before reconciliation can create a duplicate provider
submission.

## Goals

- Distinguish permanent provider/client failures from transient failures.
- Persist every attempt, retry count, next retry time, and terminal/dead-letter
  decision through the existing durable submission contract.
- Stop retrying permanent 4xx and invalid XML failures.
- Retry transient transport/server failures within the existing bounded policy.
- Reconcile an ambiguous timeout/transport result by stable provider identity
  before issuing another submission request.
- Preserve the existing provider/client seams, T004 outcome states, T015
  idempotency behavior, tenant boundaries, and runtime entry points.

## In Scope

- Provider error classification at the existing client/processor seam.
- Worker retry/ack/dead-letter decision logic and attempt metadata.
- Durable submission outcome fields already provided by the T003/T004
  submission contract.
- Optional provider reconciliation before resubmission after an ambiguous
  result.
- Focused worker and client/provider-fake regression tests.
- README and feature artifacts documenting retry behavior and the provider
  lookup limitation.

## Out of Scope

- PIH/ICV or hash-chain sequencing.
- XML mapping, invoice construction, or schema/business-rule redesign.
- UI, public queue subjects, credential changes, migrations owned by another
  worktree, or new provider endpoints.
- Changes to the existing T004/T015 stable identity algorithm.
- Live ZATCA/provider validation; deterministic fakes and HTTP fakes are the
  test boundary.

## User Scenarios & Testing

### Scenario 1: Permanent provider rejection

Given a provider returns a non-retryable 4xx response, when the worker handles
the document, then it records a terminal/rejected outcome with the attempt
number and no next retry, sends the configured alert/dead-letter signal, and
does not submit the document again.

### Scenario 2: Invalid XML

Given signing, request generation, or provider response handling identifies
invalid XML/content that cannot be corrected by retrying, when the worker
handles the document, then it records terminal failure with the validation
code/message, no next retry, and acknowledges the message as dead-lettered.

### Scenario 3: Transient failure

Given a timeout, transport failure, or retryable server response, when the
worker handles the document, then it records an ambiguous/transient outcome,
increments the attempt/retry metadata, calculates a bounded next retry time,
and retries until success or the configured maximum.

### Scenario 4: Timeout after provider acceptance

Given a submission call times out after the provider may have accepted the
stable UUID/invoice hash, when the worker is about to resubmit, then it first
queries the provider reconciliation seam. If acceptance is found, it
persists the accepted outcome and issues no second submission. If lookup is
definitively not found, it may submit again with the same stable identity. If
lookup is unsupported or ambiguous, it remains ambiguous and does not blindly
resubmit.

### Scenario 5: Bounded exhaustion

Given repeated transient failures reach the retry limit, when the final
attempt completes, then the durable outcome records terminal failure/dead
letter, the next retry is cleared, an alert is emitted, and the queue message
is acknowledged so unrelated work can continue.

## Functional Requirements

### FR-1: Error classification

The provider/client boundary MUST classify:

- permanent client/provider validation failures, including ordinary 4xx
  responses and invalid XML/content, as non-retryable;
- transient server/transport failures, including timeouts and 5xx responses,
  as retryable/ambiguous;
- accepted and accepted-with-warning responses as successful;
- unsupported or unknown failures conservatively as ambiguous unless the
  failure occurred before a provider request could be made and is known to be
  terminal.

HTTP 408, 409, 425, and 429 are treated as retryable because the provider may
have received the request or may be applying a temporary concurrency/rate
limit. Other 4xx statuses are permanent.

### FR-2: Durable attempt metadata

Each processing attempt MUST persist a monotonically increasing attempt
number, retry count, start/completion information, and next retry timestamp
when another retry is allowed. Successful and terminal outcomes MUST clear
the next retry timestamp.

### FR-3: Durable terminal/dead-letter state

Permanent failures and exhausted transient failures MUST be durably
distinguishable from retryable ambiguous work using the existing submission
outcome/error fields. They MUST record a terminal/dead-letter decision,
error/provider code, bounded error message, and no future retry timestamp.
No non-success outcome may mark the legacy document as issued.

### FR-4: Worker retry policy

The worker MUST retry only failures classified as retryable and MUST preserve
the existing maximum retry count and bounded exponential backoff. It MUST
acknowledge permanent and exhausted messages after durable recording and
notify the existing alert path. It MUST not acknowledge a transient message
before its retry budget is exhausted or processing succeeds.

### FR-5: Ambiguous reconciliation

Before any resubmission after an ambiguous timeout/transport result, the
processor MUST call the optional `SubmissionReconciler` with the stable
document UUID, invoice hash, provider reference, and logical stable key.
Accepted reconciliation results MUST complete the existing attempt without a
second `Submit` call. Definitive not-found results MAY allocate a new attempt
and reuse the same request identity. Unsupported or ambiguous lookup MUST
remain ambiguous and MUST NOT trigger blind resubmission.

### FR-6: Seam and compatibility preservation

The implementation MUST use the existing `SubmissionProvider`, client
`APIError`, `ProviderError`, `submissionStore`, `SubmissionReconciler`, queue
message, and worker entry points. It MUST not alter PIH/ICV, XML mapping, UI,
public subjects, or tenant isolation.

### FR-7: Regression tests

Focused worker/client/provider-fake tests MUST demonstrate that permanent
errors are not retried, transient errors retain retry metadata, terminal
outcomes are acknowledged/dead-lettered, and timeout-after-acceptance
reconciliation prevents a second submission. The tests MUST be capable of
showing the generic-retry behavior is red before the change.

## Non-Functional Requirements

- Durable writes remain transactional and use the exact existing T003/T004
  table and column contract.
- Error messages must not contain credentials or invoice payloads.
- Retry decisions must be deterministic and bounded; no unbounded sleeps or
  retry loops may be introduced.
- Production behavior must remain explicit when provider lookup is unavailable.
- Changes must remain isolated to T020 and one local commit; no push or PR.

## Acceptance Criteria

1. Permanent ordinary 4xx and invalid XML failures are classified as
   non-retryable and do not receive generic retries.
2. Timeout, transport, retryable 4xx, and 5xx failures remain retryable and
   preserve attempt count and next retry metadata.
3. Permanent and exhausted failures persist terminal/dead-letter evidence with
   no next retry; accepted paths remain unchanged.
4. A timeout-after-acceptance fake records one submit and one reconciliation
   call, persists acceptance, and never blindly submits twice.
5. Unsupported production lookup reports an explicit provider limitation at
   runtime and leaves the outcome ambiguous rather than issuing a duplicate.
6. Focused tests, `gofmt`, targeted tests, `go test ./...`, and `go vet ./...`
   pass; any environmental/provider lookup limitation is reported exactly.
7. README and T020 artifacts describe the behavior, and the final commit
   contains only T020 changes with no push or PR.

## Assumptions

- The T003/T004 durable contract's `outcome_state`, provider/error fields,
  `retry_count`, and `next_retry_at` columns are authoritative; no new
  backend migration is assumed.
- `OutcomeTerminalFailure` is the durable terminal/dead-letter state for
  failures that must not be replayed automatically; `OutcomeRejected` remains
  the provider business-rejection state when useful for operator reporting.
- A provider response with an invalid/undecodable cleared XML is terminal
  because resubmitting the same request cannot repair the response contract.
- The current production ZATCA client has no provider lookup endpoint; the
  existing explicit `RECONCILIATION_UNSUPPORTED` result is retained.

## Risks

- Provider-specific 4xx semantics may vary; retryable concurrency/rate-limit
  statuses are explicitly whitelisted, while other 4xx statuses are treated
  as permanent.
- A missing provider lookup endpoint prevents live timeout reconciliation; the
  daemon must fail safe rather than duplicate a submission.
- The T003 schema may not expose a dedicated boolean named dead-letter; the
  implementation must use the established terminal outcome/error fields and
  document that representation.

## Unknowns / Open Questions

- Whether deployed provider responses include a stable retry-after value. The
  default remains the existing bounded exponential backoff because no such
  field is present in the current client contract.
- Whether a future provider lookup endpoint will return a richer status than
  `ProviderResult`. The existing narrow reconciler is the compatibility
  boundary for T020.

## Observable Success Measures

- Permanent fake failures cause exactly one provider submission attempt.
- Transient fake failures expose increasing attempt/retry metadata and a
  non-zero next retry time before eventual success or exhaustion.
- Timeout-after-acceptance fake runs one submit and one reconcile operation,
  with an accepted durable outcome and no duplicate request.
- Full repository validation remains green and no out-of-scope files change.
