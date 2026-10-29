# T011 Daemon Onboarding State

## Problem

The daemon currently consumes onboarding messages, runs the full onboarding
flow, and acknowledges the message on both success and failure. The backend
now has a durable onboarding state contract, but daemon progress, failure,
retry, attempt timestamps, and completion are not written. A process failure
can therefore acknowledge work without leaving durable evidence, and a
successful onboarding does not have a durable finalization owned by the worker.

## Goals

- Make daemon onboarding progress observable through the T010 durable metadata
  contract.
- Preserve failed work until its failed state and error evidence are durable.
- Finalize successful onboarding as `done` and only then activate document
  workers.
- Keep OTP values out of durable state and preserve T029 no-OTP-cleanup
  behavior.
- Add deterministic fake-provider regression coverage for invalid OTP, provider
  outage, success, and a subsequent retry.

## In scope

- The daemon onboarding worker and onboarding processor integration.
- Writes to `branch_zatca_config` and `zatca_onboarding_attempt` using the
  T010 field and state names.
- Queued, CSR, compliance, invoices, production, done, and failed progress.
- Error code/message, retry count/eligibility, queued/started/completed and
  last-attempt timestamps.
- Acknowledgement ordering and active branch worker activation.
- Focused fake onboarding regression tests and related README documentation.

## Out of scope

- Generic invoice outcome, invoice idempotency, or invoice retry behavior.
- A new outbox, queue message protocol, backend handler, frontend behavior, or
  database migration.
- Changes to ZATCA provider APIs beyond the onboarding progress callbacks needed
  to observe existing stages.
- Reusing or persisting OTPs.

## User stories

1. As an operator, I can see queued, CSR, compliance, compliance-invoice,
   production, done, and failed onboarding progress after a daemon restart.
2. As an operator, I can see bounded error evidence and retry eligibility when
   an OTP is invalid or the provider is unavailable.
3. As the queue, a failed onboarding message is not acknowledged until the
   corresponding failed state is durably stored; if persistence fails, the
   message remains eligible for redelivery.
4. As an operator, a successful onboarding is marked done before branch
   invoice workers become active.
5. As a security boundary, the daemon never writes the one-time OTP, including
   when onboarding fails.

## Functional requirements

### FR-1: Durable queue/start state

- When an onboarding message is accepted, the daemon MUST record or refresh
  `onboard_state=queued`, `onboard_step=queued`, the current attempt number,
  `onboard_queued_at`, and `last_attempt_at` using the T010 schema.
- Existing backend-created attempt numbers MUST be preserved; a redelivery
  MUST NOT create a second attempt for the same queued attempt.

### FR-2: Stage progress

- Before CSR generation, the daemon MUST record the CSR step.
- Before the compliance CSID request, it MUST record compliance state/step.
- Before the six compliance invoices, it MUST record the invoices step.
- Before the production CSID request, it MUST record production state/step.
- Progress MUST update the current branch row and the matching attempt row by
  `(branch_id, attempt_number)`.
- Progress MUST set `onboard_started_at`/`started_at` once and refresh
  `last_attempt_at`.

### FR-3: Durable failure and retry eligibility

- An onboarding error MUST record `onboard_state=failed`, a failed step, a
  bounded non-secret error code/message, incremented retry count, and
  `last_attempt_at`.
- The matching attempt row MUST record failed state, error evidence,
  incremented retry count, and `completed_at`.
- The failed state MUST remain eligible for a later backend-created retry,
  which receives a new monotonic attempt number and cleared current error
  metadata.
- If failed-state persistence fails, the daemon MUST NOT acknowledge the
  message.

### FR-4: Durable success

- After credentials are saved successfully, the daemon MUST record
  `onboard_state=done`, `onboard_step=done`, clear current error fields, set
  `onboard_completed_at` and `zatca_onboarded_at` when absent, and refresh
  `last_attempt_at`.
- The matching attempt row MUST be finalized as done with `completed_at`.
- Document workers MUST be activated only after all done writes succeed.

### FR-5: OTP and compatibility safety

- The daemon MUST never insert or update `zatca_otp`.
- Existing T029 behavior (no legacy OTP cleanup SQL) MUST remain intact.
- Existing invoice worker acknowledgement and retry behavior MUST remain
  unchanged.

### FR-6: Regression tests

- Fake onboarding tests MUST cover invalid OTP, provider outage, success, and a
  retry after a durable failure.
- Tests MUST verify state, step, error/retry metadata, acknowledgement ordering,
  and success activation behavior.

## Non-functional requirements

- State transitions MUST be guarded by branch and attempt number so a late
  message cannot overwrite a newer backend attempt.
- Failure strings MUST be bounded and MUST NOT contain the OTP.
- The implementation MUST use the repository's existing Go and SQL
  conventions and remain compatible with Go 1.25.
- No generic invoice outcome/idempotency/retry machinery may be introduced.

## Acceptance criteria

1. Focused fake onboarding tests fail against the starting daemon because the
   state store and acknowledgement ordering do not exist.
2. The focused tests pass after implementation for invalid OTP, provider
   outage, success, and a later retry.
3. SQL writes use the exact T010 names:
   `onboard_state`, `onboard_step`, `onboard_attempt`,
   `onboard_retry_count`, `onboard_error_code`, `onboard_error`,
   `onboard_queued_at`, `onboard_started_at`, `onboard_completed_at`,
   `last_error`, `last_attempt_at`, and `zatca_onboarding_attempt`.
4. A failure-state write error leaves the message unacknowledged.
5. Done state and attempt completion are durable before document workers start.
6. `zatca_otp` is absent from onboarding SQL and the T029 schema regression
   remains green.
7. `gofmt`, targeted daemon/processor tests, and `go test ./...` pass.
8. The isolated commit contains only T011 implementation, tests, directly
   related README/spec artifacts, and no push or PR.

## Assumptions, constraints, risks, and unknowns

### Assumptions

- T010's backend always creates the current branch row and attempt row before
  publishing, so the daemon can use the current `onboard_attempt` when the
  message has no attempt field.
- `adminDB` is the daemon's write-capable connection for onboarding; the
  runtime connection is the fallback where no admin connection is configured.
- The existing `zatca.Service` can expose stage callbacks without changing its
  provider requests or result semantics.

### Constraints

- Start from ZATCA main `6021a43`.
- Inspect T010 commit `922335c` read-only and do not modify that worktree.
- Preserve T029 no-OTP-cleanup behavior.
- Do not add generic invoice reliability behavior.

### Risks

- A live MySQL server may not be available for executing the T010 migration;
  SQL contract tests must still cover the query shape and guarded updates.
- Provider errors are wrapped several times; classification must remain
  conservative and avoid leaking secrets.
- A backend-created retry can race a late old message; all daemon updates must
  include the attempt guard.

### Unknowns resolved by research

- Exact metadata names and attempt table: resolved from T010 migration and
  generated queries; see `research.md`.
- OTP cleanup compatibility: resolved from T029 diff; no cleanup SQL is allowed.
- Stage boundaries: resolved from existing `zatca.Service` flow; see
  `research.md`.

## Observable success measures

- Every accepted fake onboarding attempt has durable queued/start/progress and
  terminal metadata.
- Invalid OTP and provider outage leave `failed` state and retry evidence; no
  failure is acknowledged before that evidence is stored.
- A successful fake onboarding returns with `done` durable and active branch
  activation observed after completion.
- A subsequent retry uses a later attempt number and clears stale error state.

