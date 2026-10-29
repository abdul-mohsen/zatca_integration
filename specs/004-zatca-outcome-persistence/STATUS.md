# T004 Status

## Completed

- T004A: requirements and research created.
- T004B: design, dependency graph, tasks, and status tracking created.
- Read-only alignment completed against backend T003 commit `fc084a4`.
- T004C: fake-provider regression added; base red build failure captured.
- T004D: provider seam, production adapter, outcome classification, and stable
  document UUIDs implemented.
- T004E: transactional T003 submission/attempt/outcome/artifact store
  implemented with tenant guards and explicit schema errors.
- T004F: bill processor integration implemented; legacy bill state is set to
  `3` only for accepted outcomes.
- T004G: focused accepted/warning/rejected/pending/timeout/nil tests green.
- T004H: README updated with durable outcomes and T003 prerequisite behavior.
- T004I: `gofmt -l` reported no changed Go files needing formatting,
  `go vet ./...` passed, targeted processor tests passed (9 tests), and
  `go test ./...` passed (63 tests across 20 packages).

## In Progress

- T004J: final diff review and isolated commit.

## Blocked

- Credit/debit durable persistence is blocked by T003's `zatca_submission`
  foreign key to `bill` plus `UNIQUE (bill_id)`. T004 will return an explicit
  compatibility error for note processing rather than reuse or overwrite a
  bill submission.
- The daemon privilege script does not yet grant writes to T003 submission,
  attempt, outcome, and artifact tables or the added metadata columns. The
  required daemon grants are `SELECT, INSERT, UPDATE` on
  `zatca_submission`, `zatca_submission_attempt`,
  `zatca_submission_outcome`, and `zatca_submission_artifact`, plus
  `SELECT(merchant_id)` and `UPDATE(invoice_hash, qr_code, invoice_uuid,
  invoice_xml_path, state)` on `bill`. No legacy-only fallback is allowed.

## Backlog

- Add a backend document-kind/source-key contract for credit/debit outcomes.
- Add deployment/grant updates in the lane that owns daemon database users.
- Add provider reconciliation and hash-chain/ICV behavior in their dedicated
  tasks.

## Risks

- Existing databases without T003 migration will fail visibly at durable
  persistence start/finish.
- A provider response may omit a reference or warning code; nullable fields
  must remain empty rather than fabricated.
- Existing retries rebuild invoices; deterministic UUIDs are required to keep
  provider identity stable.
- Base red evidence at `6021a43`: `go test ./processor -run
  'TestSignSubmitUpdateWithProvider|TestProviderError' -count=1` fails to build
  because `ProviderResult`, `submissionAttempt`, `submissionOutcome`,
  `documentUpdate`, `artifactRecord`, and `submissionMeta` do not yet exist.
- Green evidence after T004: the same focused command passes all 9 tests;
  `go vet ./...` and `go test ./...` also pass.

## Next Actions

1. Review the final diff for T004 scope and remove any generated artifacts.
2. Record the T003 schema/grant blockers in the final report.
3. Commit one isolated PR-ready T004 change without pushing.
