# Research: T006 Cleared Artifact

## Repository evidence

- `processor/submit.go` saves the signed request XML and conditionally calls
  `saveXMLArtifact` for `ProviderResult.ClearedXML`, but the completion
  contract has no retained response body/evidence field.
- `processor/submission.go` updates T003 request/cleared paths and inserts
  artifact metadata, but does not write the legacy `zatca_submission.response_xml`
  compatibility column or a `response_xml` artifact.
- `processor/provider.go` normalizes `models.InvoiceResponse` into
  `ProviderResult`; the production response contains `ClearedInvoice`, status,
  warnings, errors, and invoice hash. The existing provider reference remains
  nullable because the client model has no guaranteed reference field.
- `saveXMLArtifact` currently returns success without an artifact when
  `xmlDir` is empty. That is a silent durability gap for a required artifact.
- T003's backend contract defines `request_xml`, `cleared_xml`, and
  `response_xml` artifact kinds and retains a `zatca_submission.response_xml`
  compatibility column. `cleared_xml_path` and provider reference fields are
  already present on submission and attempt rows.

## Decisions

### D1 — Carry authoritative provider response through the processor contract

Extend the normalized provider result and submission outcome with the provider
response payload/reference needed by the durable contract. Standard clearance
uses the returned cleared XML as the authoritative document artifact; reporting
keeps the signed submitted XML plus the provider response evidence.

### D2 — Use the existing artifact kinds and tenant-scoped filesystem

Write response evidence through the same tenant/company/branch path builder,
record its SHA-256 and content type, and link it to the current submission and
attempt. No backend migration or new object-store implementation is needed.

### D3 — Fail closed on required artifact storage

An empty artifact-store configuration or write failure is an explicit
processor error. The T004 completion path records `terminal_failure` when the
failure occurs before provider submission; a post-provider storage failure is
returned after durable failure completion and never treated as accepted.

## Schema/runtime dependencies

- T003 migration `0008_zatca_submission_state.sql` and grants for
  `zatca_submission`, `zatca_submission_attempt`, `zatca_submission_outcome`,
  and `zatca_submission_artifact` remain prerequisites.
- Existing runtime `XMLDir` must point to a writable durable mount. T006 does
  not add object-store credentials or deployment changes.
- No idempotency/retry/UI behavior is changed.
