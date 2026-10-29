# T017 Research

## Repository Evidence

| Question | Evidence | Decision |
|---|---|---|
| Where are fixed values introduced? | `processor/bill.go`, `credit.go`, and `debit.go` assign `DefaultPIH` and `InvoiceCounterValue: 1`; numeric `SeqNumber` can overwrite ICV independently for each document type. | Remove those assignments from production-document builders. Chain position is supplied by one branch-scoped state owner. |
| Where are PIH and ICV rendered? | `invoice/xml.go` writes `AdditionalDocumentReference` entries with IDs `ICV` and `PIH`; `Invoice.validate` already rejects an empty PIH. | Keep the XML shape, add explicit ICV validation, and apply state before `ToXML`. |
| Is a durable branch state already described? | `scripts/migration_zatca_schema.sql` and `scripts/test_setup.sql` define `branch_zatca_config.previous_invoice_hash` and `last_icv`, scoped by `branch_id`. | Use those fields as the accepted-state source of truth. A missing/uninitialized state is an error, not a fixed fallback. |
| Is a retry-safe per-document position already durable? | No table or fields store a reserved ICV, PIH, UUID, issue timestamp, or signed payload. `signSubmitUpdate` updates document metadata only after the API call. | Add the smallest explicit repository-local `zatca_chain_position` contract to the daemon migration/test schema. |
| Are document types serialized together? | `cmd/daemon/main.go` and `worker.go` start three consumers per branch (`bill`, `credit`, `debit`) with independent subjects. | Use one wildcard branch consumer for all three document subjects; retain message formats and publishers. |
| What is the accepted-result signal? | `models.InvoiceResponse` contains `Status`, `Errors`, `Warnings`, and optional validation status. Existing client returns API errors for non-2xx responses. | Treat `REPORTED`, `CLEARED`, `ACCEPTED`, `SUCCESS`, `PASS`, or validation `PASS` without errors as accepted. Unknown/non-success responses do not advance state. |
| What hash representation is available? | `crypto.InvoiceHash` returns base64-encoded SHA-256 of the ZATCA canonicalized invoice content. `sdk.SignInvoice` returns the same request hash. | Commit the signed request hash as the next PIH only after acceptance. |
| What can make a retry payload stable? | Bill dates are row data, but credit/debit processing currently uses `time.Now()` and all builders create a new UUID. SDK signatures may also vary. | Persist UUID, issue timestamp, signed XML, and invoice hash in the reservation before submission; retries reuse the prepared payload. |

## Official Semantics Applied

- PIH is the previous accepted invoice hash represented as base64 SHA-256 data.
- ICV is a decimal, digit-only counter shared by invoices, credit notes, and
  debit notes for one EGS/branch.
- Chain ownership is per EGS/branch, not per document type.
- A failed or rejected submission does not become the next accepted chain
  state.
- The initial protocol PIH is only used during explicit branch onboarding
  initialization; ordinary document processing never silently substitutes it.

## Existing Schema Contract

The daemon repository already carries the branch state columns needed by the
T016 backend concept:

```sql
branch_zatca_config.previous_invoice_hash
branch_zatca_config.last_icv
```

T017 adds a repository-local reservation table because those two accepted-state
columns alone cannot preserve a position across a crash between signing,
provider acceptance, and the final database transaction. The separate backend
must apply the equivalent contract from its T016 integration; T017 does not
edit backend source code.

## Alternatives Rejected

1. **Keep one worker per document type.** Rejected because the three workers
   can allocate the same branch position concurrently.
2. **Use an in-memory mutex only.** Rejected because process restarts and
   multiple daemon instances lose the reservation and retry identity.
3. **Increment `last_icv` before submission.** Rejected because failed
   submissions would advance accepted state and could create an unowned PIH.
4. **Reuse `SeqNumber` as ICV.** Rejected because it is a document/business
   number and is not the shared EGS counter.
5. **Fallback to the current fixed PIH/ICV.** Rejected by the requirement and
   unsafe for an already-used EGS.

## Resolved Risks

- Missing chain columns/table now fail with an explicit migration/contract
  error.
- Out-of-order acceptance is rejected by an atomic commit check comparing the
  reservation with the accepted branch state.
- Duplicate accepted acknowledgements are idempotent by reservation key and
  accepted hash.
