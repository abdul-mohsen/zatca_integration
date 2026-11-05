# Research: PR33 D10 Identity and Recovery

## Repository evidence

### D10 identity

- `processor/query.go` reads `bill.sequence_number` into `BillRow.SeqNumber`.
- `processor/bill.go` currently constructs `invoice.Invoice.ID` as
  `INV-{bill.id}`. This is derived from the database primary key and ignores
  `BillRow.SeqNumber`.
- `invoice/xml.go` writes `Invoice.ID` directly to the root `cbc:ID` element.
- `invoice.Invoice` documents `ID` as the invoice number, while
  `InvoiceCounterValue` is a separate ICV field. Therefore the XML ID and ICV
  are already conceptually distinct; the builder is the remaining mismatch.
- `queue/chain.go` stores `signed_xml`, `invoice_uuid`, `invoice_icv`,
  `previous_invoice_hash`, `invoice_hash`, and `invoice_kind` in
  `zatca_submission`. The same transaction updates the bill's
  `invoice_uuid`, `invoice_hash`, and `qr_code`, and advances the branch chain.
- There is no known dedicated `zatca_submission.document_id` column in this
  repository. The immutable signed XML is therefore the durable copy of the
  XML document ID, while `bill.sequence_number` remains the source/printed
  identity. Adding an unverified schema column would exceed scope.

**Decision:** use the non-empty, trimmed `bill.sequence_number` as the XML
invoice ID and reject a bill with no usable sequence before booking. Do not
derive a replacement from the database row ID, UUID, or ICV. Keep existing
submission storage unchanged; it already persists the signed XML and the
separate UUID/ICV/hash fields.

### Provider and recovery behavior

- `queue.OrderedStream.invoiceFor` commits the signed invoice before provider
  submission. A later delivery reads `SQLChain.Stored`, so the invoice is not
  rebuilt and the branch chain is not booked again.
- A technical submit or outcome-recording failure calls `failed`, which releases
  the claim and leaves the delivery unacknowledged. This is the retry path.
- `queue.OrderedStream.process` records an outcome before `finish` completes the
  claim and acknowledges the delivery. If completion fails after recording,
  redelivery sees the recorded outcome and does not submit again.
- `processor.responseOutcome` already rejects
  `validationResults.errorMessages`, records warning-only responses as
  accepted-with-warning, and otherwise accepts a clean 2xx response.
- `processor.rejectionOutcome` already treats HTTP 409 as
  `OutcomeAmbiguous` and returns `ErrOutcomeAmbiguous`; other retryable
  transport/auth/rate/server failures stay technical, while ordinary invoice
  4xx responses are rejections.
- Existing focused tests cover clean/warning/error 2xx classification,
  ambiguous 409, stored-XML redelivery after submit failure, recorded-answer
  no-resubmit, and the operator alert. They do not prove the XML ID uses the
  source sequence, and they do not cover explicit top-level invoice errors or
  a record-failure redelivery that becomes ambiguous.

**Decision:** preserve the existing durable-before-ack protocol, add the
missing regression tests, and make explicit invoice error/status signals on a
2xx response non-accepting. No provider lookup or sandbox behavior is invented;
an unresolved 409 remains an operator reconciliation outcome.

## Alternatives considered

1. **Keep `INV-{bill.id}` as the XML ID.** Rejected because it makes the
   database surrogate key the printed/provider document number and ignores the
   branch sequence already loaded from the source record.
2. **Use the ICV as the XML ID.** Rejected because ICV is the fiscal chain
   counter and is already serialized separately in the ICV additional document
   reference.
3. **Add a new durable schema column for XML document ID.** Rejected for this
   change because tenant schema ownership/migrations are outside this worktree.
   The signed XML is already durable and contains `cbc:ID`.
4. **Treat HTTP 409 as accepted.** Rejected because a duplicate/ambiguous
   response does not prove that the local invoice was accepted or cleared.
5. **Retry a 409 automatically.** Rejected because blind retries can repeat a
   provider submission after an acceptance that was lost locally. Reconciliation
   is required.

## Remaining external evidence

- No local test can prove the live ZATCA sandbox's exact duplicate-UUID
  response or whether a specific invoice payload is accepted. Those require
  credentials and an external sandbox run.
- No local test can prove the provider's operational reconciliation endpoint is
  available for this deployment. The code therefore reports ambiguous outcomes
  for manual/operator reconciliation.
