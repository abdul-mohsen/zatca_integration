# T017 Design

## Architecture

The processor owns a narrow `ChainStateStore` boundary. The production
implementation is a MySQL store backed by:

1. `branch_zatca_config.previous_invoice_hash` and `last_icv` for the latest
   accepted state of one branch/EGS.
2. `zatca_chain_position` for the durable document reservation and prepared
   request.

The processor obtains one reservation before rendering XML, applies the
reservation to the invoice, persists the signed request before submission, and
commits the accepted response and document metadata atomically.

## State Model

```text
branch accepted state
  (previous_invoice_hash, last_icv)
             │
             ├─ reserve(branch, doc_type, doc_id)
             │    └─ (pih, icv, uuid, issue_at)
             ├─ save prepared signed XML/hash
             └─ commit accepted(hash, uuid, qr)
                    ├─ document state = 3
                    ├─ branch previous_invoice_hash = accepted hash
                    ├─ branch last_icv = reserved icv
                    └─ reservation status = accepted
```

`zatca_chain_position` has one primary key per `(branch_id, doc_type,
document_id)` and a unique `(branch_id, icv)` constraint. Reserved rows are
reused on retries. An accepted row is idempotent when the same accepted hash
is observed again.

## Reservation

`SQLChainStateStore.Reserve` runs a transaction:

1. Lock the branch configuration row.
2. Return an existing reservation for the document, including its stable UUID,
   issue time, signed XML, and accepted status.
3. Otherwise read the greatest reserved/accepted ICV for the branch, choose the
   next decimal position, and copy the current accepted PIH.
4. Insert a `reserved` row. The branch accepted state is not advanced.

Missing branch state, missing reservation table, duplicate identity, or an
uninitialized PIH returns an explicit error. No fixed production fallback is
selected.

## Prepared Request and Retry Safety

The first attempt signs the rendered XML and stores signed XML plus invoice
hash in the reservation before calling ZATCA. A retry loads that exact signed
payload and does not create a new UUID, issue timestamp, ICV, or PIH. This
preserves the provider identity across an accepted response whose result was
lost locally.

## Accepted Commit

`CommitAccepted` runs one transaction:

1. Lock the reservation and return success if it is already accepted with the
   same hash.
2. Lock the branch state and verify that the reservation PIH equals the
   current accepted PIH and that its ICV is exactly `last_icv + 1`.
3. Update the document's hash/QR/UUID/state and the branch's accepted PIH/ICV.
4. Mark the reservation accepted with the accepted hash.
5. Commit all changes together.

An out-of-order or conflicting acknowledgement rolls back and leaves accepted
state unchanged. Non-accepted provider responses do not call `CommitAccepted`.

## Processor Integration

- `BuildBillInvoice`, `BuildCreditNoteInvoice`, and `BuildDebitNoteInvoice`
  no longer assign PIH/ICV defaults or use row sequence numbers as the EGS
  counter.
- Each `Process*` function reserves a position for its branch and document
  type, applies it to the invoice, and delegates to the shared submission
  pipeline.
- The shared pipeline accepts a `ChainStateStore`, persists the prepared
  signed payload, classifies the provider result, and commits accepted state.
- Debit notes use the `debit_note` table and note ID for the accepted metadata
  transaction when that schema is enabled.

## Queue Integration

The daemon starts one durable wildcard consumer per `(tenant, branch)` using
`zatca.*.<db>.<branch>`. Existing message subjects remain unchanged, so
backend publishers do not need a new event format. The single consumer
serializes invoice, credit-note, and debit-note processing in arrival order.

## Explicit Schema Contract

The daemon migration/test schema adds:

```sql
zatca_chain_position(
  branch_id, doc_type, document_id, icv, previous_invoice_hash,
  invoice_uuid, issue_at, signed_xml, invoice_hash, status, accepted_at
)
```

The separate T016 backend must apply the equivalent table or expose an
equivalent atomic reservation/commit adapter before production rollout. The
daemon fails with a migration-contract error rather than silently using
constants.

## Failure Handling

- Signing or preparation persistence failure: no provider request is made.
- HTTP/provider failure or non-accepted response: reservation remains
  `reserved`; accepted branch state is unchanged and a retry reuses it.
- Accepted response followed by local failure: the persisted reservation
  reuses the exact request on retry; the first successful commit is
  idempotent.
- Out-of-order commit: return an error and do not overwrite newer state.

## Test Strategy

- Invoice XML tests assert non-default PIH/ICV values, decimal ICV output, and
  rejection of empty/zero chain values.
- Processor tests use an in-memory implementation of the same store boundary
  to verify cross-document-type allocation, branch isolation, concurrent
  reservation uniqueness, and idempotent accepted commits.
- Existing package tests plus `go test ./...` and `go vet ./...` cover
  integration regressions.

## Traceability

| Requirement | Design element |
|---|---|
| FR1–FR4 | `ChainStateStore`, explicit PIH/ICV application, XML validation |
| FR5 | One wildcard branch consumer and branch-scoped SQL locks |
| FR6–FR8 | Reservation status, atomic accepted commit, strict state checks |
| FR9 | Explicit migration errors and no fixed fallback |
| FR10 | Shared pipeline keeps existing signing/API/document metadata behavior |
