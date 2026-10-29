# T015 Design

## Boundary

```text
T014 live/replay envelope or legacy payload
        -> queue.Message normalization + stable key
        -> JetStream stable message identity
        -> worker
             -> tenant/document-scoped durable gate
             -> processor stable provider UUID/request
             -> optional provider reconciliation
             -> T004 outcome/attempt persistence
```

The queue and worker remain source-compatible. The T004 processor store is the
durable idempotency boundary; a process-local map is not authoritative.

## Message normalization

`queue.Message` gains the T014 canonical fields while retaining all legacy
top-level fields. `Normalize` fills canonical values from aliases and derives:

- `stable_document_key = <tenant>:<branch>:<doc-type>:<source-id>` for normal
  documents;
- an OTP-specific key for onboarding;
- deterministic `event_id` and `attempt_id`;
- `attempt = attempt_count + 1` when omitted.

Conflicting aliases fail validation. `StableDocumentKey()` and
`NATSMessageID()` are used by replay publication and worker correlation.

## Replay publication

Startup replay serializes the normalized additive envelope and calls
`PublishMsgID` with the stable document key. A live T014 publication and its
replay therefore share the same JetStream identity. The subject remains
`zatca.<doc-type>.<db-name>.<branch-id>`.

## Stable provider identity

`config.Config` carries optional message context (`BranchID`,
`StableDocumentKey`, `EventID`, `OutboxID`, `Attempt`). The worker copies the
branch configuration before processing a message, so cached branch credentials
are not mutated.

Invoice builders retain T004's deterministic tenant/document UUIDv5 seed for
both legacy and versioned deliveries. The T014 stable key is persisted as
correlation metadata and gates duplicate processing; it does not change the
existing provider identity. The resulting UUID is the T003
`zatca_submission.document_id` and the `models.InvoiceRequest.UUID` sent to the
provider.

## Durable gate and state machine

Extend `submissionAttempt` with the loaded current state and flags:

- `resolved`: accepted or accepted-with-warning; return without a provider
  call;
- `inFlight`: a locked recent pending attempt; return a typed duplicate result;
- `needsReconciliation`: the existing ambiguous attempt is reused for lookup;
- otherwise allocate the next attempt as T004 did.

`StartSubmission` performs the lookup and attempt allocation in one transaction
using fixed SQL and `FOR UPDATE`. It verifies merchant and document identity.
The latest pending attempt is considered in-flight for a bounded lease; stale
pending rows follow the existing retry path. A separate optional
`StartRetrySubmission` creates a new attempt only after an ambiguous identity
lookup returns not-found.

Accepted duplicates return the stored hash when available, otherwise the
stable provider document UUID. In-flight duplicates return
`ErrDuplicateDelivery`, which the worker treats as an acknowledged duplicate
without changing retry/dead-letter policy.

## Ambiguous reconciliation

Before `Submit`, if `needsReconciliation` is set:

1. Build the same deterministic request UUID/hash.
2. Verify a stored request hash, when present, matches the current request.
3. If the provider implements `SubmissionReconciler`, call it with
   `ProviderIdentity`.
4. On accepted or accepted-with-warning, complete the existing attempt with
   that outcome and do not call `Submit`.
5. On `(nil, nil)` (not found), allocate a new attempt and call `Submit` with
   the same UUID/hash.
6. On unsupported/transport error, complete an ambiguous outcome and return;
   never issue a blind second request.

The production adapter implements the optional method by returning
`RECONCILIATION_UNSUPPORTED`. This is an explicit provider limitation until a
query endpoint exists.

## Persistence metadata

Request artifact metadata includes `stable_document_key`, `event_id`,
`outbox_id`, `attempt`, `attempt_count`, and `attempt_id` when available.
This keeps T014 correlation durable without changing T003 tables. Existing
provider fields, outcomes, and bill state safety remain unchanged.

## Test strategy

- Queue tests decode a full T014 envelope and a legacy payload, assert the
  same canonical identity, and verify stable replay message IDs.
- Processor tests use a fake store/provider to prove duplicate accepted
  deliveries issue one submit call.
- A timeout-after-acceptance test proves one submit call plus one reconciliation
  call, accepted durable completion, stable UUID/hash, and no second submit.
- A not-found reconciliation test proves the subsequent request reuses the
  prior UUID rather than creating a new identity.
- Existing T004 outcome tests remain unchanged and continue to pass.

## Failure, security, and rollout

- No payload, credentials, or OTP is logged as an identity.
- Missing T003 tables/columns remain actionable errors.
- The production provider's missing query capability is surfaced as an
  ambiguous outcome; it is not silently downgraded to a new request.
- T014 rollout is additive because top-level legacy aliases remain accepted.
- T020 retry classification/dead-letter and T017 PIH/ICV are untouched.

## Requirement traceability

| Requirement | Design element |
|---|---|
| FR-1 | Queue envelope superset and alias validation |
| FR-2 | Stable key plus deterministic provider UUID |
| FR-3 | SQL durable gate with `FOR UPDATE` and state flags |
| FR-4 | Provider identity reuse and content/hash verification |
| FR-5 | Optional reconciliation before any new submit |
| FR-6 | Existing conditional legacy state update |
| FR-7 | Unchanged subjects/runtime APIs and explicit out-of-scope tasks |
| FR-8 | Queue/processor focused red-green fake tests |
