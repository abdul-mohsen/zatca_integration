# Requirements: ZATCA/C3 Cleared Artifact

**Feature:** T006 — retain authoritative ZATCA artifacts
**Base:** daemon outcome commit `670be54`
**Dependency:** T004 provider seam and durable outcome persistence

## Problem

The T004 processor records the submission outcome and request artifact, but the
durable contract does not prove that the provider's authoritative response
artifact was retained. A standard clearance response contains the stamped XML
that must be delivered and audited; losing it or accepting a storage failure
silently makes a successful outcome operationally unsafe.

## Goals

- Retain the authoritative cleared/stamped XML returned by standard clearance.
- Retain provider response/reference data for clearance and reporting in the
  existing submission, attempt, outcome, and artifact contract.
- Keep the signed submitted XML and provider response evidence for reporting.
- Make artifact filesystem/object-store failures explicit, with the existing
  terminal/retryable outcome classification rather than a silent success.
- Preserve T004 outcome mapping, tenant scoping, and runtime entry points.

## In scope

- `processor` provider result, artifact persistence, and completion contract.
- Standard clearance and simplified reporting paths used by T004.
- A focused fake-provider processor regression for returned cleared XML.
- Related README and feature status/spec artifacts.

## Out of scope

- Idempotency, retry scheduling/reconciliation, or queue changes.
- UI/API changes, backend migrations, provider credentials, or live ZATCA
  claims.
- Credit/debit schema compatibility work already blocked by T003.

## Functional requirements

1. A successful standard clearance MUST persist the exact returned cleared XML
   as a `cleared_xml` artifact and expose its durable path in the submission
   and attempt records.
2. The provider response/reference MUST remain linked to the same submission
   and attempt through the existing provider fields and response artifact
   contract; missing optional provider values remain nullable.
3. Reporting MUST retain the signed submitted XML and provider response
   evidence; it MUST NOT require a cleared XML.
4. An artifact write or object-store operation MUST return an actionable error.
   The processor MUST not mark the document issued or return success when the
   authoritative artifact cannot be retained.
5. Existing T004 state mapping remains unchanged: only accepted and
   accepted-with-warning set legacy state `3`.

## Acceptance criteria

- The new fake-clearance test fails before the implementation because the
  returned cleared XML is not present in the durable completion contract.
- The same test passes after implementation and checks exact XML content,
  cleared artifact kind/path, and provider reference.
- Reporting behavior and all existing T004 processor tests remain green.
- `gofmt`, targeted processor tests, `go test ./...`, and `go vet ./...` pass.
- The final commit contains only T006 changes and is not pushed.

## Assumptions, risks, and unknowns

- T003's exact artifact kinds are `request_xml`, `cleared_xml`,
  `response_xml`, and `other`; no migration is expected.
- The current runtime uses a tenant-scoped filesystem path as the artifact
  store. An object-store adapter is not introduced in this task.
- The provider client currently exposes a typed response but no guaranteed
  provider reference field. Existing nullable provider-reference behavior must
  be preserved rather than fabricating one.
- A storage failure after provider acceptance can leave an external provider
  submission unknown to the worker. T006 records the failure explicitly;
  reconciliation/idempotency remains a later task.
