# T015 Research

## Repository evidence

### Consumer and replay paths

- `cmd/daemon/worker.go` JSON-decodes `queue.Message` and sends every
  document to `processor.ProcessBill`, `ProcessCreditNote`, or
  `ProcessDebitNote`. It currently does not inspect outbox, attempt, or stable
  document fields.
- `cmd/daemon/replay.go` publishes legacy five-field JSON and uses
  `subject-id` as the JetStream message ID. T014 uses the durable
  `stable_document_key` as the message identity, so the current replay ID does
  not deduplicate against a backend live publication.
- JetStream consumers are durable per tenant/branch/document subject, but this
  is not a durable business-state idempotency guarantee. A redelivery can
  reach the processor after a prior attempt has completed.

### T014 envelope contract (read-only backend commit `5e2c986`)

The backend retains these top-level legacy aliases:

`doc_type`, `id`, `branch_id`, `db_name`, and optional `otp`.

The additive canonical fields include:

`contract`, `version`, `schema_version`, `event_id`, `tenant_id`, `egs_id`,
`document_type`, `document_id`, `source_id`, `outbox_id`,
`stable_document_key`, `attempt`, `attempt_count`, and `attempt_id`.

The stable key for documents is
`<tenant>:<branch>:<document-type>:<source-id>`. Outbox retries keep that key
and change only attempt metadata. The backend sets the same stable key as the
JetStream `Nats-Msg-Id`. The daemon must not require nested-only fields because
the compatibility aliases remain the migration contract.

### T004 durable submission behavior

- `processor/submission.go` persists `zatca_submission.document_id` as the
  deterministic invoice UUID and records attempts/outcomes in the T003 tables.
- `StartSubmission` currently always resets the current row to `pending` and
  creates a new attempt, even if the row is already accepted, ambiguous, or
  has a pending attempt.
- `CompleteSubmission` updates the bill row and only sets `state = 3` for
  accepted and accepted-with-warning.
- The T003 schema has no `stable_document_key`, `outbox_id`, or event-attempt
  columns on `zatca_submission`; it does have `document_id`, merchant/bill
  identity, provider reference, current outcome state, and child attempt rows.
- Request artifact metadata is JSON and can retain the T014 correlation fields
  without altering the backend schema.

### Provider capability

- `SubmissionProvider` wraps signing, request generation, QR generation, and
  report/clear submission.
- The production client exposes only POST reporting and clearance calls. It
  has no provider lookup/query endpoint and `InvoiceResponse` has no stable
  provider reference field.
- The invoice UUID and invoice hash in `models.InvoiceRequest` are the stable
  provider identity available to the daemon.

## Resolved decisions

### D1: Decode a superset envelope in `queue.Message`

Add T014 canonical fields and normalization helpers to the daemon's existing
message type. Legacy payloads derive a stable key from tenant, branch,
document type, and source ID. Conflicting canonical/legacy aliases are
rejected; missing additive fields are filled deterministically.

### D2: Use the durable provider document UUID as the T003 lookup identity

The T003 schema does not store the T014 stable key as a column. The daemon
will preserve T004's deterministic provider UUID (tenant, document kind, and
source ID) for both legacy and versioned deliveries. The T014 stable key is
the delivery/deduplication identity, not a replacement for the existing
provider document UUID. That UUID is persisted in
`zatca_submission.document_id` and is used for tenant-scoped state lookup and
provider reconciliation.

The T014 stable key, outbox ID, event ID, and attempt metadata are retained in
request-artifact JSON metadata. This is additive and avoids an unsupported
schema migration.

### D3: Gate every new attempt on durable current state

`StartSubmission` will lock and inspect the existing submission row before
allocating an attempt:

- accepted/accepted-with-warning: return a resolved duplicate result;
- a recent pending attempt: return an in-flight duplicate result without a
  provider call;
- ambiguous: return the existing attempt for reconciliation;
- rejected, terminal failure, or stale pending: preserve existing worker
  retry behavior by allocating a new attempt.

The accepted and in-flight short-circuits are tenant- and document-scoped.
The SQL transaction remains the concurrency boundary; no process-local map is
treated as the source of truth.

### D4: Reconcile ambiguity through an optional provider capability

Add `SubmissionReconciler` with a lookup keyed by the stable request UUID,
invoice hash, provider reference, and standard/cleared mode. A fake can return
an accepted result after a timeout. The production adapter reports an explicit
`RECONCILIATION_UNSUPPORTED` limitation because the current ZATCA client has no
lookup endpoint. An unsupported lookup leaves the durable state ambiguous and
does not issue a second request.

If a reconciler returns `(nil, nil)`, the identity was not found; the daemon
may allocate a new attempt and submit the same stable request identity. Any
new request therefore reuses the same UUID and hash identity rather than
creating a fresh provider document.

### D5: Keep T020 and T017 out of the change

The existing exponential retry loop and outcome classification remain in
place. T015 only suppresses duplicate work and adds the reconciliation gate.
No retry/dead-letter policy or PIH/ICV sequencing is changed.

## Alternatives considered

1. **Only rely on JetStream `Nats-Msg-Id`** — rejected because the duplicate
   window is not durable and legacy/live messages may not share the same
   identity.
2. **Use a process-local map of processed keys** — rejected because it fails
   across daemon restarts and multiple workers.
3. **Add a new backend migration for stable keys** — rejected because T015 is
   daemon-only and T014/T003 already provide a stable provider document UUID
   and JSON artifact metadata.
4. **Always resubmit after an ambiguous error** — rejected because a timeout
   can follow provider acceptance and duplicate fiscal issuance.
5. **Invent provider references or a lookup URL** — rejected because the
   current provider client exposes neither; the limitation is explicit.

## Remaining limitations

- True timeout reconciliation cannot run against the current production
  adapter until a provider query endpoint is added. The daemon will not
  silently submit again when reconciliation is unsupported.
- T003's bill-keyed schema still cannot durably represent credit/debit note
  submissions independently; T015 preserves T004's explicit compatibility
  error for those paths.
- T014 `outbox_id` and attempt metadata are not dedicated T003 columns; they
  are persisted in request artifact metadata and used for message correlation.
