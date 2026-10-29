# T004 Design

## Boundary

```text
queue worker/runtime APIs
        -> processor.ProcessBill (unchanged entry point)
        -> provider seam
             -> production adapter -> existing SDK + client
             -> fake provider in processor tests
        -> SQL submission store
             -> T003 current submission row
             -> T003 attempt/outcome/artifact history
             -> legacy bill metadata/state
        -> tenant-scoped XML/artifact filesystem paths
```

The daemon remains the owner of signing, provider submission, and evidence
references. The backend T003 worktree remains read-only and owns the migration
and monitor read contracts.

## Provider seam

`processor.SubmissionProvider` is the smallest interface needed by the shared
sign/submit path:

- sign XML and return signed XML plus hash;
- generate a typed invoice request from signed XML;
- extract QR from signed XML;
- submit a request through reporting or clearance.

The production adapter wraps `*zatca.Service`, so `main`, worker functions,
and existing `ProcessBill`/note signatures remain unchanged. The adapter
normalizes `models.InvoiceResponse` into a provider result with:

- explicit T003 outcome state;
- status/provider code;
- first warning code/message;
- first rejection/error code/message;
- cleared XML when returned.

Provider errors are typed. Timeout/transport errors are `ambiguous`; explicit
4xx/API rejection errors are `rejected`; pre-provider signing/request failures
and nil responses are `terminal_failure`. A fake may return `pending` or any
valid state directly.

## Stable identity

Invoice builders use UUIDv5 over the tenant ID, document kind, and source row
ID. The UUID is valid for ZATCA and remains unchanged across worker retries.
The same value is written to the provider request and T003
`zatca_submission.document_id`.

The T003 `merchant_id` value is sourced from `tenant.Tenant.ID`, carried into a
new numeric `config.Config.MerchantID` field, and kept separate from the
existing string `Config.TenantID` used for filesystem isolation.

## Processing data flow

1. `ProcessBill` builds a stable invoice and calls `signSubmitUpdate`.
2. The SQL store ensures the one bill submission row, calculates the next
   attempt number, inserts a pending attempt and pending outcome, and keeps the
   current submission state pending.
3. The processor generates XML, signs it, and saves the signed request XML at
   the existing tenant/company/branch/bill path. The path and SHA-256 are
   retained as a `request_xml` artifact.
4. The typed request is submitted through reporting or clearance. QR is
   extracted from the signed XML when available. A returned cleared XML is
   base64-decoded, saved beside the request as a `-cleared.xml` artifact, and
   its path is recorded.
5. A single completion transaction:
   - closes the attempt with state/provider/error/retry fields;
   - appends the final outcome linked to that attempt;
   - updates current submission state/provider/retry/timestamp/path fields;
   - inserts artifact rows;
   - updates bill UUID/hash/QR/XML path;
   - sets bill `state = 3` only for `accepted` or
     `accepted_with_warning`.
6. Non-success outcomes return an error after their durable completion. The
   worker's existing retry/alert behavior remains intact, and each retry
   appends a new attempt/outcome instead of replacing the stable identity.

If signing, request generation, or provider submission fails, the same
completion transaction records the classified non-success outcome and any
evidence available before returning the error. A QR extraction failure remains
non-fatal because the signed XML is still authoritative; its absence is
recorded by leaving QR nullable/empty.

## SQL persistence

The production store uses only fixed internal SQL templates with `?`
parameters. Table/column names are selected from a closed document-kind map;
no user input is interpolated. The store uses transactions for start and
completion:

- Start: `INSERT ... ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id)`, select
  the next attempt number, insert attempt and pending outcome.
- Completion: update attempt and current submission, insert final outcome and
  artifacts, then update the bill row before commit.

Required exact T003 names are not probed dynamically and are not silently
skipped. A missing table/column, permission failure, incompatible foreign key,
or unsupported note-document relationship is returned as an actionable error.

## Credit/debit compatibility

The current T003 table has a foreign key to `bill` and `UNIQUE (bill_id)`.
That cannot represent a credit/debit note independently from its source bill.
T004 therefore refuses durable note processing with an explicit compatibility
error rather than reusing or overwriting the bill submission row. The existing
note processor entry points and provider seam remain compiled and available;
note persistence requires a future backend document-kind/source-key contract.
This blocker is tracked in `STATUS.md`.

## Failure handling

| Condition | State | Legacy document state |
|---|---|---|
| accepted, no warnings | `accepted` | set to `3` |
| accepted with warnings | `accepted_with_warning` | set to `3` |
| provider rejection/errors | `rejected` | unchanged |
| timeout/transport uncertainty | `ambiguous` | unchanged |
| explicit pending/empty definitive status | `pending` | unchanged |
| nil result/sign/request/unsupported schema | `terminal_failure` or explicit compatibility error | unchanged |

Provider code/warning fields remain null when the provider did not return a
value. Error code/message are retained in attempt fields and rejection code/
message on the current submission where applicable.

## Test strategy

- Add a processor fake provider and in-memory submission-store fake for
  deterministic accepted, warning, rejected, timeout/ambiguous, and nil cases.
- Assert outcome state, provider code/warning, attempt number, UUID/hash/QR,
  artifact references, and the `state=3` rule.
- Add a base regression test before implementation and run it at `6021a43` to
  capture red evidence.
- Run gofmt, the focused processor test, all processor tests, and `go test ./...`.

## Documentation and rollout

README processing-pipeline text will describe explicit outcome persistence and
the prerequisite T003 migration/grants. No provider credentials or invoice
payloads enter source or test fixtures. Deployment applies T003 first; if it
is absent, daemon processing fails visibly instead of degrading to legacy-only
writes.

## Requirement traceability

| Requirement | Design element |
|---|---|
| FR-1 | `SubmissionProvider` plus production adapter |
| FR-2 | typed response/error normalization and T003 state writes |
| FR-3 | current/attempt/outcome provider fields |
| FR-4 | stable UUID, request/cleared artifacts, metadata transaction |
| FR-5 | conditional legacy `state=3` update |
| FR-6 | next attempt number and retry metadata |
| FR-7 | unchanged runtime entry points and numeric tenant context |
| FR-8 | base red test and fake-provider regression suite |
