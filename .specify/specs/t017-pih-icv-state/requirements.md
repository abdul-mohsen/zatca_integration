# T017 Requirements: Per-EGS/Branch PIH and ICV State

## Problem

The daemon currently emits ZATCA invoice, credit-note, and debit-note XML using
fixed PIH/ICV defaults. That breaks the required per-EGS hash-chain and
counter semantics, especially when multiple branches or document types are
processed. Accepted submissions must advance the correct branch's chain
without losing state when a request is retried or processed concurrently.

## Goals

- Generate PIH and ICV from the state belonging to the active tenant EGS and
  branch, rather than from constants.
- Serialize chain positions for invoices, credit notes, and debit notes within
  each EGS/branch sequence.
- Persist accepted hash-chain state only after the submission is accepted, and
  make that update idempotent/retry-safe.
- Preserve the existing XML/signing/submission flows outside this state
  handling.

## In Scope

- State contract for the active tenant, EGS/branch, next ICV, and accepted
  previous invoice hash.
- XML generation of PIH as the base64 SHA-256 hash-chain value and ICV as a
  digit-only counter.
- Per-branch serialization of invoice, credit-note, and debit-note positions.
- Accepted-result state advancement with protection against duplicate
  acknowledgements and stale/out-of-order updates.
- Focused XML and processor tests covering non-default PIH/ICV values,
  branch isolation, document-type ordering, and retry-safe state advancement.
- A repository-local integration contract when durable tenant fields cannot be
  changed in this repository.

## Out of Scope

- Discounts and line-item pricing changes.
- Party/customer mapping.
- New retry policy or queue topology changes unrelated to chain-state
  correctness.
- User-interface changes.
- Backend/database implementation in the separate T016 repository.
- Pushes, pull requests, or changes outside this worktree.

## User Scenarios and Acceptance Tests

### Scenario 1: Continue an existing branch sequence

Given an EGS/branch state with a non-default next ICV and accepted previous
hash, when the next invoice is rendered, then its ICV equals that state's next
counter and its PIH equals the stored accepted hash, not `1` or a fixed
placeholder.

### Scenario 2: Serialize mixed document types

Given invoice, credit-note, and debit-note messages for one EGS/branch, when
they are processed concurrently, then exactly one chain position is allocated
at a time and the resulting positions are monotonic across all three document
types.

### Scenario 3: Keep branches independent

Given two branches under the same tenant/EGS, when each branch processes a
document, then each branch uses and advances only its own PIH/ICV state.

### Scenario 4: Advance only after acceptance

Given a rendered/submitted document, when ZATCA rejects or the submission
fails, then the branch's accepted PIH/ICV state is unchanged. When the same
accepted result is observed more than once, then the state is advanced once
and remains consistent.

### Scenario 5: Keep the retry payload stable

Given a document position allocated for submission, when the submission is
retried, then it reuses the same PIH/ICV and does not allocate a new chain
position merely because the first attempt did not receive an accepted result.

## Functional Requirements

1. The daemon MUST obtain PIH and ICV from an explicit state provider keyed by
   tenant and EGS/branch identity; no fixed PIH or ICV fallback is allowed.
2. The state provider MUST expose enough information to render the next
   chain position and to commit an accepted result.
3. ICV values MUST be serialized as digit-only decimal counters.
4. PIH values MUST be the base64 representation of the SHA-256 hash-chain
   value required by ZATCA XML semantics.
5. Allocation and acceptance MUST be serialized per EGS/branch across
   invoice, credit-note, and debit-note processing.
6. Failed, rejected, or otherwise non-accepted submissions MUST NOT replace
   the last accepted PIH or advance the accepted ICV.
7. Repeated acknowledgement of the same accepted document MUST be harmless
   and MUST NOT advance the sequence twice.
8. A stale or out-of-order acknowledgement MUST NOT overwrite newer accepted
   state.
9. State errors MUST be explicit and actionable; the daemon MUST fail closed
   rather than silently selecting a constant initial state.
10. Existing document XML and processor behavior outside PIH/ICV state MUST
    remain unchanged.

## Non-Functional Requirements

- State updates MUST be safe under concurrent processing for one branch.
- State transitions MUST be deterministic and testable without a live ZATCA
  endpoint.
- The implementation MUST not require changes to the separate backend
  repository.
- The implementation MUST document any repository-local integration contract
  needed for durable tenant state.
- Existing supported Go tests and vet checks MUST remain passing.

## Success Criteria

- Focused regression tests fail when PIH/ICV are fixed at `1` and pass when
  state is non-default.
- Mixed invoice/credit/debit processing for one branch produces a strictly
  increasing sequence with no duplicate committed positions under concurrent
  execution.
- Two branches can advance independently without changing each other's
  PIH/ICV state.
- A failed submission leaves accepted chain state byte-for-byte unchanged,
  while duplicate accepted acknowledgements leave one committed transition.
- `gofmt`, targeted tests, `go test ./...`, and `go vet ./...` pass.

## Compatibility and Integration Constraints

- Start from ZATCA main commit `6021a43` on branch
  `fix/zatca/h3-pih-icv-ordering-v2`.
- Conceptually align with backend T016 atomic per-branch/EGS sequence and PIH
  ownership at commit `b602c05`, but do not edit or depend on that repository's
  files directly.
- If durable tenant DB fields are unavailable here, the repository-local
  contract MUST make the required state explicit and identify the backend
  integration dependency; it MUST not use a fixed fallback.

## Assumptions, Risks, and Unknowns

### Assumptions

- The processor receives or can request an EGS/branch identity for every
  document.
- The accepted submission result contains the document hash (or enough
  information to derive the accepted hash) needed for the next PIH.
- The existing XML builder can accept PIH/ICV as inputs without changing
  unrelated invoice semantics.

### Risks

- The current repository may not have durable tenant state fields; an
  explicit adapter contract may be required until backend T016 is integrated.
- Existing processor signatures may be shared by CLI and daemon paths, so
  state wiring must preserve compatibility without hiding missing state.
- Incorrect ordering between reservation, XML generation, submission, and
  commit can create gaps or duplicate positions.

### Unknowns to Resolve in Research

- Where fixed PIH/ICV values are currently introduced and which call paths
  render invoice, credit-note, and debit-note XML.
- Whether a durable state repository already exists in this repository.
- What result/status information is available to distinguish accepted from
  failed or rejected submissions.
- The exact official XML field and hash representation behavior used by the
  existing implementation.

