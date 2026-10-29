# T025 Debit-Note Processing Design

## Boundaries and data flow

```text
queue.DocDebit / startup replay
  -> QueryDebitNoteByID(debit_note.id)
  -> LoadProducts(parent bill id)
  -> reserve branch chain position (T017)
  -> BuildDebitNoteInvoice(type 383, BillingReferenceID=INV-parent-bill-id)
  -> apply PIH/ICV
  -> sign, save request XML, generate API request/QR
  -> report or clear through existing provider selection
  -> persist accepted debit source outcome + XML path + chain state
```

The queue subject and retry loop remain unchanged. The source ID in a debit
message is always the `debit_note.id`; the parent bill ID is used only to load
the invoice context and products.

## Component changes

### Processor query

Use the existing bill/customer/product projection and join `debit_note` by
source ID. Preserve nullable fields needed by existing builders. Pending
replay selects `debit_note.id` where `state=1` and the parent belongs to the
requested branch. A missing table/column returns an explicit database error.

### Debit builder

Reuse seller/customer/line/payment/note mappings. Set type code 383 and use
the source ID for the debit invoice identity. Set the billing reference to
the parent bill's existing `INV-{bill_id}` identifier. Chain values are
applied only after reservation. Existing XML generation emits the billing
reference and KSA-10 reason.

### Submission and persistence

Use `signSubmitUpdate` with `row.NoteID.Int64`, `debit_note`, and
`invoice_qr`. Save signed XML in the existing tenant/company/branch/debit
artifact directory. Extend the accepted chain update with an optional XML
path and write `invoice_xml_path` for the owning source. Existing bill and
credit callers pass the same optional field, preserving their behavior.

No separate debit retry loop, provider abstraction, or UI status is added.

### Queue

Retain `queue.DocDebit`, the existing branch document subject list, worker
switch, replay publisher, and message ID deduplication. No new subject or
consumer is introduced.

## Failure handling

- Query/schema errors propagate to the existing retry wrapper.
- Chain reservation/signing errors do not call the provider.
- Provider failures retain existing retry/ack behavior.
- Accepted persistence failures return an error and leave the existing chain
  store transaction semantics in control.
- Missing T024 schema is reported as an integration error; no parent-bill
  fallback is attempted.

## Test strategy

- Builder regression for type 383, reason, party/line mapping, discount
  compatibility, and original bill reference.
- SQL-mock query/replay regressions proving the source table and source IDs
  are used.
- Persistence regression proving accepted debit metadata targets
  `debit_note` and includes the signed XML path.
- Existing queue tests remain the contract for subject and branch routing.

## Requirement traceability

| Requirement | Design element |
|---|---|
| FR-1 | Debit source query and pending replay query |
| FR-2–FR-3 | Type-383 builder and shared invoice/XML helpers |
| FR-4 | T017 chain reservation/application |
| FR-5 | Existing standard/simplified submit selection |
| FR-6 | Optional XML path in accepted source update |
| FR-7 | Existing queue and mixed branch consumer |
| FR-8 | Explicit schema error propagation |
