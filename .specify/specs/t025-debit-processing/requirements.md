# T025 Debit-Note Processing Requirements

## Problem

Debit-note queue messages and startup replay must be processed as durable debit
documents after the backend creates the T024 debit-note source. The daemon
must not reject debit messages merely because the old backend lacked a
`debit_note` table, nor may it update the parent bill as if it were the debit
note.

## Goals

- Query a debit note and its parent bill context by debit-note source ID.
- Build a type-383 debit-note document using the existing invoice, party,
  discount, and XML conventions.
- Reference the original bill document, not the debit-note source or an
  unrelated sequence value.
- Sign and submit the debit note through the existing branch-ordered pipeline.
- Persist accepted debit-note outcome fields and the signed XML artifact path
  on the debit-note source.
- Include debit messages in normal and startup queue handling.
- Add focused regression coverage for the unsupported lookup path and debit
  document identity.

## Scope

### In scope

- `processor` debit query, builder, submission metadata, and persistence
  integration.
- Debit startup replay and branch queue routing where required for the T024
  source contract.
- Focused debit processor regression tests.
- Related runtime dependency documentation.

### Out of scope

- Retry/backoff policy redesign.
- UI or backend handler changes.
- New provider APIs or a replacement signing architecture.
- Changes to credit-note behavior except where a shared helper must remain
  compatible.

## User stories

### Story 1: Process a queued debit note

As the ZATCA daemon, when a debit message identifies a durable debit-note
source, I query the source and parent bill, create the correct debit XML,
sign it, submit it using the existing standard/simplified decision, and
return the accepted invoice hash.

### Story 2: Preserve debit outcome artifacts

As an operator, after accepted debit processing, I can find the debit-note
UUID, invoice hash, QR value, submitted state, and signed XML path on the
debit-note source.

### Story 3: Recover pending debit work

As the daemon, after restart, I republish pending debit-note IDs and route
them through the same branch-ordered worker as bills and credit notes.

## Functional requirements

- **FR-1 Query**: A debit message ID is resolved against `debit_note.id`; the
  query must join its parent bill and preserve parent branch, company,
  sequence, customer, payment, and product context.
- **FR-2 Identity**: The generated document has type code 383 and a stable
  debit-note source identity. The billing reference identifies the original
  bill document emitted by the bill builder.
- **FR-3 Content**: Debit reason, seller, buyer, payment, line, discount, and
  XML-required fields follow existing processor and invoice conventions.
- **FR-4 Chain**: Debit processing reserves and applies the shared branch PIH
  and ICV position from T017; no fixed production PIH or ICV is introduced.
- **FR-5 Submission**: Standard buyers use the existing clearance path and
  simplified buyers use the existing reporting path.
- **FR-6 Persistence**: Accepted debit processing updates only the debit-note
  source outcome/artifact columns and shared chain state, including
  `invoice_uuid`, `invoice_hash`, `invoice_qr`, `state`, and
  `invoice_xml_path` when the T024 schema is present.
- **FR-7 Queue**: Debit messages remain on the existing subject format, are
  handled by the branch consumer, and are replayed when `debit_note.state=1`.
- **FR-8 Compatibility**: Missing T024 debit schema is reported as a
  runtime integration error; it is not silently treated as success or as a
  parent-bill submission.

## Non-functional requirements

- Preserve existing retry and acknowledgement behavior.
- Keep tenant and branch isolation of database rows and filesystem artifacts.
- Do not add a provider call before the source query and chain reservation
  succeed.
- Keep changes narrowly scoped to T025.

## Acceptance criteria

1. A regression test demonstrates that the old unsupported debit lookup path
   fails before the T025 implementation and the new query succeeds with the
   T024-shaped source.
2. A debit builder test asserts type 383, reason, seller/buyer mapping, and
   original bill reference.
3. Accepted debit processing uses the debit-note ID/table, not the parent bill
   ID/table, for outcome persistence.
4. Signed XML is saved under the debit artifact location and its path is
   persisted when the schema supports it.
5. Pending debit replay and branch queue routing publish `queue.DocDebit`
   messages with the debit-note ID.
6. `gofmt`, targeted processor tests, `go test ./...`, and `go vet ./...`
   pass.
7. The isolated commit contains no UI, retry-policy, or unrelated backend
   changes.

## Assumptions, constraints, risks, and unknowns

- **Assumption**: T024 migrations 0011 and 0012 are applied before production
  debit processing; the daemon must remain buildable without a live database.
- **Assumption**: The bill document identifier is the existing
  `INV-{bill_id}` value produced by `BuildBillInvoice`.
- **Constraint**: T017 branch chain state and `zatca_chain_position` remain the
  source of PIH/ICV truth.
- **Risk**: A runtime database that has only the legacy debit table will fail
  the new query or persistence update until T024 migration 0011 is applied.
- **Risk**: The provider may accept a request while local outcome persistence
  fails; existing chain reservation/retry handling must remain unchanged.
- **Resolved unknown**: T024 persists parent `sequence_number` and EGS
  context on `debit_note`, but the UBL billing reference should use the
  original bill document ID, not that display sequence.
