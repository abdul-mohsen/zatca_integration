# T004 Research

## Repository Evidence

### Existing daemon pipeline

- `processor/bill.go` owns `ProcessBill` and the shared
  `signSubmitUpdate` path used by bill, credit-note, and debit-note workers.
- The base path calls concrete `*sdk.SDK` and `*client.Client` methods through
  `*zatca.Service`; there is no replaceable provider boundary.
- The base path writes the signed XML, submits the request, and then executes
  one dynamic document update that always includes `state = 3`. Provider
  warnings are logged only. A nil response and provider errors are returned
  before any durable outcome record exists.
- `config.Config.TenantID` is currently a string used to scope XML paths. The
  daemon already loads the numeric master tenant ID as `tenant.Tenant.ID`, but
  does not pass it into processor persistence.

### Existing provider API

- `models.InvoiceResponse` exposes `Status`, `InvoiceHash`,
  `ClearedInvoice`, `Warnings`, and `Errors`; it has no provider-reference
  field.
- `client.ReportInvoice` and `client.ClearInvoice` return a typed
  `*client.APIError` for non-2xx responses and wrap transport errors.
- A successful reporting response is normally `REPORTED`; a successful
  clearance response is normally `CLEARED`. A non-empty warning list is
  available independently of the success status.
- A cleared invoice is base64-encoded in `ClearedInvoice`; the production
  adapter can decode and save it without changing the existing client API.

### T003 durable contract (read-only worktree)

Backend commit `fc084a4` defines:

- `zatca_submission` columns: `bill_id`, `merchant_id`, `status`,
  `document_id`, `outcome_state`, provider code/warning/reference fields,
  rejection fields, submitted/cleared/outcome timestamps, retry fields,
  request/cleared XML paths, and lifecycle timestamps.
- `zatca_submission_attempt`: unique `(submission_id, attempt_number)`,
  explicit attempt state, provider/error fields, retry fields, request/cleared
  paths, and start/completion timestamps.
- `zatca_submission_outcome`: append-only state/provider/reference rows linked
  to a submission and optional attempt.
- `zatca_submission_artifact`: request/cleared/response/other artifact
  references with path, content type, sha256, metadata, and creation time.
- Allowed state values are exactly `pending`, `accepted`,
  `accepted_with_warning`, `rejected`, `ambiguous`, and `terminal_failure`.
- T003 monitor queries apply both `bill.merchant_id` and
  `zatca_submission.merchant_id` tenant predicates.

### Tenant and schema compatibility

- The master tenant registry provides `Tenant.ID int64`, `Tenant.DBName`, and
  the tenant database connection. The T003 `merchant_id` column is `int`, so
  production persistence must use the master numeric ID rather than the
  database-name string.
- The daemon's current `scripts/create_db_users.sql` grants only legacy bill
  and credit-note metadata updates. It does not grant writes to the T003
  submission/attempt/outcome/artifact tables or the T003 metadata columns.
- T003's durable table has `UNIQUE (bill_id)` and a foreign key to `bill`.
  This supports one durable submission per bill but cannot independently
  represent multiple credit/debit-note submissions tied to the same bill
  without an additional backend schema contract.

## Decisions

### D1: Introduce a processor-level provider seam

Define a small submission-provider interface covering signing, API-request
construction, QR extraction, and reporting/clearance. Wrap the existing
`zatca.Service` for production. Keep `ProcessBill`, `ProcessCreditNote`, and
`ProcessDebitNote` signatures unchanged; tests invoke the shared processor
with a fake provider.

**Reason:** The processor can test all outcome branches without Docker, SDK
files, credentials, or live HTTP while preserving runtime wiring.

### D2: Use typed outcome/error classification

Normalize provider responses into the six T003 states. Treat `REPORTED` and
`CLEARED` without warnings as `accepted`; any warning makes the result
`accepted_with_warning`; provider errors make it `rejected`; timeout or
transport uncertainty makes it `ambiguous`; signing/request/nil-response
failures make it `terminal_failure`. A fake may return explicit `pending` or
any allowed state to exercise the persistence mapping.

**Reason:** A non-success result must never be treated as issued, and
transport uncertainty must not be collapsed into rejection.

### D3: Persist pending before provider submission

The SQL store will ensure the stable submission row, create the next attempt,
and append a `pending` outcome before signing/submitting. It will finish the
attempt and append the final outcome in one transaction. Each retry keeps the
same document identity and gets a new attempt number.

**Reason:** A daemon crash before the provider call remains observable, and
attempt history is not overwritten.

### D4: Use deterministic document UUIDs

The current invoice builders generate a random UUID on every processing retry.
For T004, derive a UUIDv5 from tenant identity, document kind, and source row
ID so retries reuse the same UUID. The provider request and
`zatca_submission.document_id` therefore remain stable.

**Reason:** T003 requires stable document identity and provider deduplication
needs the same UUID on retry.

### D5: Update legacy metadata on every outcome, state only on success

Persist UUID/hash/QR and signed-request path for all outcomes where available.
Set the legacy row's `state = 3` only for `accepted` and
`accepted_with_warning`; leave the pre-existing non-success state unchanged.

**Reason:** Operators need evidence for rejected/ambiguous work without
misrepresenting fiscal issuance.

## Alternatives Considered

1. **Keep concrete `*zatca.Service` calls and mock HTTP/SDK**: rejected because
   it requires live-like setup and cannot deterministically exercise signing,
   nil, and timeout branches at processor level.
2. **Use only legacy document columns**: rejected because T003 monitor reads
   durable outcome/attempt/artifact tables and this would silently lose state.
3. **Use database name as `merchant_id`**: rejected because T003 declares an
   integer tenant key and backend monitor predicates use the numeric key.
4. **Overwrite one submission row on every retry**: rejected because attempts
   and outcomes are append-only in T003.
5. **Treat all provider errors as rejected**: rejected because timeout/transport
   errors are ambiguous and may have reached ZATCA.
6. **Create a second submission row for a credit/debit note using the source
   bill ID**: rejected because T003 enforces unique `bill_id` and would
   conflate documents or fail with a duplicate.

## Remaining Uncertainty / Explicit Compatibility Blocker

T003's current durable contract has no document-kind/source-note key and
enforces `UNIQUE (bill_id)`. Therefore this T004 lane can persist bill
submissions safely, but it cannot independently persist a credit note or debit
note tied to a bill that already has a durable submission without a backend
schema change. The implementation MUST NOT silently reuse or overwrite the
bill's submission row for those note types. The blocker is recorded in
`STATUS.md` and the design/tasks; a future backend contract must add a
document-kind/source ID (or equivalent) before note outcomes are wired.

The daemon's privilege script also does not yet include T003 writes. T004 will
make the required SQL explicit and document the exact grants needed; it will
not catch permission/schema errors and fall back to legacy-only persistence.
