# T025 Debit-Note Processing Research

## Evidence reviewed

| Question | Evidence | Decision |
|---|---|---|
| Why is debit processing unavailable on the old path? | The pre-`a2b4c06` `QueryDebitNoteByID` returned `debit_note table does not exist`, and `QueryPendingDebitNotes` returned no IDs. | Use the T024 debit source query and pending-state replay query. |
| Which identifier owns chain/outcome state? | `a2b4c06` uses `row.NoteID.Int64` for `DocumentDebit` and the `debit_note` table. | Preserve source-ID ownership; never update the parent bill. |
| What is the original document reference? | `BuildBillInvoice` emits `INV-{bill.ID}`. T024 stores parent `sequence_number` as context, not as the rendered UBL document ID. | Debit `BillingReferenceID` must be `INV-{row.ID}`. |
| Which schema does the daemon need? | T024 `98ae215` adds `debit_note` source/output columns and generic submission source identity; T017 adds branch chain state and `zatca_chain_position`. | Document both migrations as runtime prerequisites. |
| How are parties, discounts, and XML rendered? | `buildCustomer`, `buildLines`, `invoice.Invoice.ToXML`, and existing line/document allowance patterns are shared processor/XML helpers. | Reuse shared helpers; do not introduce a debit-specific XML format. |
| How is queue ownership defined? | `a2b4c06` changes to one `zatca.*.<tenant>.<branch>`-filtered consumer and routes bill/credit/debit through `processMessage`. | Keep existing subjects, mixed branch consumer, and retry/ack behavior. |
| What does accepted persistence currently do? | `SQLChainStateStore.CommitAccepted` updates source hash/QR/UUID/state but not `invoice_xml_path`; `signSubmitUpdate` writes the XML file before submit. | Carry the saved XML path into accepted source persistence without changing retry policy. |

## Alternatives considered

1. **Keep the unsupported path until all tenants migrate.** Rejected: it
   silently drops durable debit work and contradicts the T024 source contract.
2. **Treat a debit note as its parent bill.** Rejected: it corrupts the
   parent outcome and breaks source identity/idempotency.
3. **Copy the full outcome/retry subsystem from the parallel C2 branch.**
   Rejected for T025: it redesigns retry and submission state outside this
   task. T025 only adds the source/artifact persistence required by the
   existing T017 pipeline.
4. **Use the parent display sequence in `BillingReference`.** Rejected:
   it is not the document ID emitted by `BuildBillInvoice`.

## Remaining uncertainty

- Production rollout must confirm that backend deployment applies T024
  migrations 0011/0012 and the T017 chain-state equivalent before enabling
  debit consumers. This is an operational dependency, not a reason to add
  fallback behavior.
