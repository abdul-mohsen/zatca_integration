# T015: Idempotent ZATCA Consumer and Ambiguous-Outcome Reconciliation

## Problem

The daemon can receive the same ZATCA delivery more than once: a live
publication and startup replay can describe the same document, and a message
may be redelivered after acknowledgement or worker failure. The current
worker starts a new submission attempt for every delivery. A provider timeout
can also leave the provider having accepted the invoice while the daemon
cannot observe the response; blindly issuing a new request can create a second
provider submission.

The daemon must consume the versioned T014 envelope without breaking legacy
five-field messages, use the stable logical document identity for durable
deduplication, and reconcile an ambiguous prior outcome before it sends a new
provider request.

## Goals

- Decode T014 versioned envelopes while preserving legacy decoder compatibility.
- Deduplicate live/replay and redelivered deliveries using the stable document
  key and durable submission identity.
- Consult durable submission/outcome/attempt state before signing or submitting.
- Reuse a provider identity/request when the durable state proves it is safe.
- Reconcile an ambiguous provider result using the provider's identity lookup
  capability before issuing a new request.
- Add focused worker/provider-fake tests that are red before implementation and
  green after it.

## In Scope

- Queue message decoding and canonical identity extraction in the daemon.
- Worker-level duplicate-delivery guards for live and replay messages.
- Durable lookup/claim behavior around `zatca_submission`,
  `zatca_submission_attempt`, and outcome state.
- Stable document UUID/request identity reuse for the same tenant, document
  kind, and source ID.
- A narrow provider reconciliation capability for checking a previously
  submitted stable identity after an ambiguous response.
- Focused fake-provider tests for duplicate delivery and
  timeout-after-acceptance.
- T015 feature artifacts and documentation/status updates.

## Out of Scope

- Retry classification, backoff policy redesign, or dead-letter handling
  (T020). Existing worker retry behavior remains unchanged except that an
  already-resolved duplicate is not resubmitted.
- PIH/ICV/hash-chain sequencing (T017).
- Backend T014 changes, migrations, generated code, or outbox behavior.
- New provider endpoints or credentials not supported by the current provider
  contract.
- Credit/debit persistence that cannot be represented by the T003 bill-keyed
  schema.
- Changes to public queue subjects or daemon runtime entry points.

## User Scenarios

### Scenario 1: Live and replay duplicate

Given a live event and a startup replay event refer to the same tenant,
document type, source ID, and stable document key, when both are delivered,
then exactly one provider submission is issued and the duplicate delivery is
acknowledged or treated as an already-resolved result.

### Scenario 2: Duplicate after acceptance

Given durable state already records an accepted or accepted-with-warning
submission, when the same delivery is redelivered, then the daemon consults
durable state, does not call the provider again, and preserves the accepted
legacy state.

### Scenario 3: Timeout after provider acceptance

Given a provider accepts a request but the response is lost, when processing
records an ambiguous outcome and the worker retries, then the daemon queries
the provider by stable document/provider identity; if the provider reports
acceptance, the daemon persists that reconciled outcome and does not issue a
new request.

### Scenario 4: Ambiguous result not found

Given an ambiguous prior attempt and reconciliation finds no provider result,
then the daemon may issue a new request only after the lookup has completed,
using the same stable provider identity. If lookup is unsupported or itself
ambiguous, the daemon leaves the outcome ambiguous and returns an actionable
result without a blind request. T015 does not redefine retry classification.

### Scenario 5: Legacy event

Given a legacy payload containing only `doc_type`, `id`, `branch_id`, and
`db_name`, when it is consumed, then the daemon derives the same stable
document identity and processes it without requiring T014 fields.

## Functional Requirements

### FR-1: Envelope compatibility

The consumer MUST accept T014 envelopes with `stable_document_key`,
`outbox_id`, and `attempt` fields, while also accepting legacy top-level
fields. Canonical values MUST be validated for consistency with compatibility
aliases when both are present.

### FR-2: Stable identity

The daemon MUST derive one stable identity from tenant, document kind, and
source ID (or use the validated T014 stable document key). The identity MUST
remain unchanged across live/replay delivery, worker retry, and provider
reconciliation.

### FR-3: Durable deduplication

Before creating a new provider request, the daemon MUST consult the durable
submission/outcome state for the stable identity. Resolved accepted outcomes
MUST short-circuit duplicate deliveries. Concurrent or already-pending work
MUST NOT create a second provider request for the same stable identity.

### FR-4: Provider identity reuse

When durable state contains the same stable document/provider identity and the
provider request can be safely reconstructed, the daemon MUST reuse it rather
than generating a new identity. No reuse is allowed across tenant, document
kind, source ID, or document-content mismatch.

### FR-5: Ambiguous reconciliation

After a timeout or transport uncertainty, the daemon MUST attempt provider
reconciliation using a stable provider identity before issuing another
submission request. A positive provider result MUST be persisted as accepted
or accepted-with-warning. A negative/not-found result MUST remain distinguishable
from an accepted result.

### FR-6: State safety

Duplicate and reconciliation paths MUST preserve the existing T004 rule:
only accepted and accepted-with-warning outcomes set legacy document state to
submitted. Ambiguous, pending, rejected, and terminal outcomes MUST NOT do so.

### FR-7: Scope preservation

Existing worker APIs, queue subjects, tenant isolation, and T004 outcome
fields MUST remain usable. T015 MUST NOT implement T020 retry classification
or T017 PIH/ICV behavior.

### FR-8: Regression evidence

Focused worker/provider-fake tests MUST fail against commit `670be54` because
duplicate deliveries resubmit or ambiguous outcomes do not reconcile before a
new request. The same tests MUST pass after implementation.

## Non-Functional Requirements

- No credentials or invoice payloads may be included in identity or logs.
- SQL used for identity lookup/claiming MUST use fixed parameterized templates.
- Duplicate handling MUST be tenant-scoped and safe under concurrent workers.
- Legacy messages MUST remain decodable during backend rollout.
- Schema/provider limitations MUST be reported precisely; no silent legacy-only
  fallback may hide a missing T003 field or reconciliation capability.
- Changes MUST remain isolated to the T015 worktree and one local commit.

## Acceptance Criteria

1. T014 envelopes and legacy messages decode to one canonical document identity.
2. Live/replay and redelivered duplicate deliveries issue at most one provider
   submission and consult durable state before processing.
3. An accepted durable outcome short-circuits duplicate work without a provider
   call.
4. A timeout-after-acceptance fake proves reconciliation finds acceptance and
   no second submission request is issued.
5. Provider identity/request UUID remains stable across retries and is reused
   only when tenant/document identity and content match.
6. Focused tests are red at `670be54` and green after the implementation.
7. `gofmt`, targeted tests, `go test ./...`, and `go vet ./...` pass, or exact
   blockers are recorded.
8. No T020 retry/dead-letter or T017 PIH/ICV changes appear in the diff.
9. README and T015 status/spec artifacts describe the behavior and any schema
   or provider limitation.
10. One isolated local commit is created; no push or pull request is made.

## Assumptions

- T014 `stable_document_key` is the durable logical event identity and remains
  stable across outbox attempts.
- T004 `zatca_submission.document_id` is the stable provider/document UUID for
  bill submissions, and `outcome_state`/attempt rows are available for lookup.
- A provider reconciliation operation can be added as an optional narrow
  capability without changing existing production API calls; if the current
  ZATCA API has no lookup endpoint, the limitation must be explicit.
- Legacy daemon messages may not carry outbox or attempt metadata and must use
  deterministic fallback identity.

## Risks

- The T003 schema may not have a unique stable-key column; a lookup may need to
  use the existing merchant-scoped `document_id` and source bill key.
- The current client API exposes submit/report/clear but may not expose a
  provider lookup by document UUID, limiting true timeout reconciliation.
- Existing in-memory fake stores do not model concurrent SQL claims; worker
  tests must prove the decision logic without claiming production concurrency
  guarantees beyond the SQL transaction.
- A provider can accept an old request while a new payload has changed; identity
  reuse must reject content mismatches.

## Unknowns / Open Questions

- Which exact T014 envelope fields should be persisted in T004 tables versus
  used only for correlation?
- Does the deployed provider expose a query endpoint or only submit endpoints?
- Can the existing T003/T004 schema atomically claim an in-flight submission
  without adding a migration?
- Should an ambiguous not-found result permit a new request immediately or only
  after the existing worker retry policy; T015 permits a new request only after
  a successful, definitive not-found lookup and reuses the same identity.

## Observable Success Measures

- One duplicate-delivery test records one provider submission call.
- One timeout-after-acceptance test records one submit call, one reconciliation
  call, and a durable accepted outcome.
- Stable document/provider identity is equal across all attempts in those tests.
- Legacy decoder tests continue to pass and full repository validation remains
  green.
