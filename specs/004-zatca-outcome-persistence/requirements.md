# Requirements: ZATCA Daemon Outcome Persistence

**Feature:** T004 — ZATCA/C2 outcome persistence  
**Base:** ZATCA main `6021a43`  
**Prerequisite:** Backend T003 `fc084a4` (read-only alignment only)

## Problem

The daemon currently signs and submits a document, then unconditionally writes
the legacy submitted state. Provider responses, transport uncertainty, retry
attempts, and generated artifacts are not durably recorded in the submission
state introduced by T003. A non-success provider result can therefore appear
issued, and operators cannot distinguish an accepted document from a warning,
rejection, ambiguous timeout, or terminal failure.

## Goals

- Give daemon processing a narrow provider/SDK boundary that can be replaced by
  deterministic fakes in processor tests.
- Persist every supported provider outcome and the provider codes/warnings
  associated with it.
- Persist stable document identity, attempts, retry timing/counts, and request,
  cleared, response, and QR/hash/UUID evidence using the T003 names.
- Mark the invoice's legacy submitted/issued state only for accepted or
  accepted-with-warning outcomes.
- Preserve tenant context, existing queue/runtime APIs, and existing document
  processing entry points.

## In Scope

- Bill and credit-note sign/submit processing used by the daemon.
- The existing debit-note path to the extent its current schema/query contract
  permits it.
- Provider/SDK seam and its production adapter.
- Durable writes to T003 submission, outcome, attempt, and artifact records,
  plus existing invoice metadata columns.
- Focused processor regression tests for accepted, accepted-with-warning,
  rejected, timeout/ambiguous, and nil provider results.
- Documentation of the daemon's persisted outcome behavior.

## Out of Scope

- Changes to the backend T003 worktree or its migrations, queries, generated
  code, or monitor handlers.
- New queue subjects, public runtime APIs, deployment changes, UI changes, or
  provider credentials.
- Provider-dependent sandbox evidence; fakes prove deterministic daemon logic
  only.
- Hash-chain/ICV sequencing, debit-domain completion, or retry reconciliation
  beyond recording the current attempt/outcome data.

## User Scenarios & Testing

### Scenario 1: Accepted submission

**Given** signing and provider submission return an accepted result, **when**
the daemon processes the document, **then** it stores the stable UUID/hash,
QR and request artifact references, records an accepted attempt/outcome, and
sets the legacy document state to submitted.

### Scenario 2: Accepted with warning

**Given** the provider accepts the document with a warning code/message,
**when** processing completes, **then** the durable state is
`accepted_with_warning`, warning fields are retained, and the legacy document
state is submitted.

### Scenario 3: Rejected submission

**Given** the provider returns a rejection, **when** processing completes,
**then** the durable state is `rejected`, provider/rejection details and
artifacts are retained, the processor returns an error suitable for the worker,
and the legacy document is not marked submitted.

### Scenario 4: Timeout or ambiguous transport result

**Given** the provider call times out or otherwise cannot establish whether the
provider received the document, **when** processing completes, **then** the
durable state is `ambiguous`, the attempt is closed with its retry metadata,
artifacts remain available, and the legacy document is not marked submitted.

### Scenario 5: Nil provider result

**Given** the provider boundary returns no result without a successful
response, **when** processing completes, **then** the daemon records a
terminal failure (or an explicitly recorded provider error if the operation
cannot be persisted), returns an error, and does not mark the document
submitted.

### Scenario 6: Tenant isolation

**Given** two tenants process documents with overlapping identifiers, **when**
the daemon persists artifacts and outcomes, **then** each record and filesystem
path retains its tenant context and no tenant's data is substituted for
another's.

## Functional Requirements

### FR-1: Fakeable provider boundary

The processor MUST depend on a small replaceable boundary for signing,
request construction, QR extraction, and reporting/clearance submission. The
production path MUST continue to use the existing SDK/client behavior, while
tests MUST be able to supply a fake without a live SDK, HTTP server, or
provider.

### FR-2: Explicit outcome mapping

The processor MUST persist exactly the T003 operational states:
`pending`, `accepted`, `accepted_with_warning`, `rejected`, `ambiguous`, and
`terminal_failure`. Accepted and accepted-with-warning are success outcomes;
rejected, ambiguous, and terminal failure are not.

### FR-3: Durable provider details

For each provider attempt, the processor MUST retain available provider code,
warning code/message, provider reference, error code/message, attempt number,
start/completion time, retry count, and next retry time. Missing provider
values MUST remain nullable/empty rather than fabricated.

### FR-4: Durable identity and artifacts

The processor MUST retain the document UUID and invoice hash, QR when
available, request XML path, cleared/response XML path when available, and
artifact records linked to the submission and attempt. Paths MUST retain the
existing tenant/company/branch scoping.

### FR-5: Legacy state safety

The processor MUST NOT set a bill or note's legacy state to submitted for
rejected, ambiguous, terminal-failure, nil, signing, request-generation, or
provider-transport errors. Existing metadata writes MUST NOT erase durable
outcome evidence.

### FR-6: Attempt and retry behavior

Each processing attempt MUST receive a monotonically increasing attempt number
for the stable submission/document. Retry count and next retry time MUST be
persisted consistently with the current worker retry behavior. A later attempt
MUST NOT replace the document identity or previous attempt evidence.

### FR-7: Runtime compatibility

Existing tenant lookup, `queue.Message`, worker, `ProcessBill`,
`ProcessCreditNote`, and `ProcessDebitNote` entry points MUST remain usable.
Tenant context MUST be passed to all persistence and artifact operations.

### FR-8: Regression evidence

The focused processor fake-provider regression tests MUST fail on base
`6021a43` because non-success handling or provider persistence is absent or
incorrect, then pass after T004. Targeted tests and `go test ./...` MUST be
run after implementation.

## Non-Functional Requirements

- SQL identifiers used by the daemon MUST match T003 exactly; dynamic SQL MUST
  be explicit and parameterized except for a closed internal table/column map.
- No silent fallback may omit T003 persistence when required columns/tables are
  missing. Any unresolved schema compatibility MUST be recorded precisely.
- Existing public/runtime APIs and tenant boundaries MUST be preserved.
- Error messages MUST remain actionable and must not include credentials or
  invoice payloads.
- Changes MUST remain isolated to the T004 worktree and commit; no push or PR
  operation is part of this task.

## Acceptance Criteria

1. A narrow production provider adapter and fakeable processor boundary exist
   without changing the existing runtime entry points.
2. Accepted and accepted-with-warning outcomes persist provider details,
   attempt/outcome rows, UUID/hash/QR, request/cleared/response artifact paths,
   and set legacy document state to submitted.
3. Rejected, timeout/ambiguous, terminal, and nil outcomes persist explicit
   non-success state and details without setting legacy document state to
   submitted.
4. Tenant ID is present in durable identity/path writes and existing tenant
   runtime APIs continue to compile.
5. Focused fake-provider processor tests demonstrate red on `6021a43` and green
   after the change for accepted, warning, rejected, timeout, and nil cases.
6. `gofmt`, targeted processor tests, and `go test ./...` pass; any unresolved
   dynamic SQL/schema compatibility is documented with exact names and impact.
7. README and feature status/spec artifacts describe the implemented behavior.
8. The final commit contains only T004 changes, is PR-ready, and is not pushed.

## Assumptions

- T003's durable table and column names are authoritative:
  `zatca_submission`, `zatca_submission_attempt`,
  `zatca_submission_outcome`, `zatca_submission_artifact`,
  `merchant_id`, `document_id`, `outcome_state`, provider fields, retry fields,
  and request/cleared XML path fields.
- The existing legacy bill/credit-note `state=3` means successfully submitted
  and remains the compatibility signal for accepted outcomes only.
- The existing SDK/client can be wrapped without changing their public methods.
- Existing database users will be granted the T003 columns/tables by the
  backend deployment lane; T004 will not silently fall back to legacy-only
  writes.

## Risks

- This daemon branch's historical schema and T003's schema may differ in
  table names or foreign-key types; any mismatch must be reported rather than
  hidden by best-effort SQL.
- The current provider client exposes status and warnings but not a dedicated
  rejection/timeout taxonomy; the adapter must classify transport errors
  conservatively.
- The legacy `zatca_submission` row lifecycle may be owned by another runtime
  component; the processor must use an idempotent upsert/lookup strategy.

## Unknowns / Open Questions

- Whether the deployed tenant database already creates `zatca_submission` rows
  before daemon processing or expects the daemon to create them. Research must
  inspect the T003 schema and current daemon grants before implementation.
- Whether a cleared XML is returned for every accepted standard invoice or only
  for clearance; the design must preserve it when present and leave the field
  null otherwise.
- Whether T003's `retry_count` represents zero-based attempts or completed
  retries. Default: preserve existing worker semantics and record attempt
  number separately.

## Observable Success Measures

- Each fake-provider case produces one inspectable durable outcome and does not
  falsely issue a non-success document.
- Accepted/warning results expose provider codes/warnings and artifact paths
  through the exact T003 columns.
- Full repository tests remain green and the final git diff contains no T003
  worktree changes.
