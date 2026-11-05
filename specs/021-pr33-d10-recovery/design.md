# Design: PR33 D10 Identity and Recovery

## Boundaries

The change is limited to the bill pipeline's document-ID selection, provider
response classification, and focused queue/processor tests. Existing SQL
transactions, claim leases, and acknowledgement order remain the source of
truth for recovery.

## Document identity flow

1. `QueryBillByID` loads `bill.sequence_number` into `BillRow.SeqNumber`.
2. Bill verification normalizes and requires this value before `Book`.
3. `buildBillInvoice` sets `invoice.Invoice.ID` to that value.
4. `invoice.ToXML` serializes it as the root `cbc:ID`.
5. Signing and `SQLChain.Commit` retain the resulting signed XML alongside the
   UUID, ICV, previous hash, invoice hash, and source bill fields.
6. `SQLChain.Stored` returns the immutable signed XML for retries; it never
   rebuilds the XML or allocates another ICV.

The database row ID remains a source-record locator only. The ICV remains the
branch chain counter and is serialized in the existing ICV document reference.

## Provider outcome flow

`BillPipeline.Submit` maps a successful response through one classifier:

- validation error messages, explicit provider error details, or an explicit
  failed validation status -> rejected;
- warning details or warning validation status -> accepted with warning;
- no error/warning signal -> accepted.

HTTP errors are handled separately:

- 409 -> ambiguous outcome plus `ErrOutcomeAmbiguous`;
- ordinary invoice 4xx -> rejected;
- auth, timeout, rate-limit, server, transport, decoding, or nil-response
  failures -> technical retry.

No automatic provider lookup is introduced. An ambiguous outcome is stored and
reported for reconciliation.

## Recovery state transitions

```text
claim
  -> verify/book/build/commit
       -> technical failure: release claim, leave delivery unacknowledged
       -> stored invoice -> submit
            -> technical failure: release claim, leave delivery unacknowledged
            -> outcome -> record before complete/ACK
                 -> record failure: release claim, retry
                 -> recorded outcome: complete and ACK
```

On redelivery, an invoice with a recorded outcome is completed without a
provider call. An invoice committed without an outcome uses the exact stored
signed XML. If the provider accepted before a local crash and responds 409 on
the replay, the result is recorded as ambiguous rather than guessed.

## Test strategy

- Unit tests in `processor/bill_test.go` assert identity and response
  classification without a provider.
- Ordered-stream tests use the existing fakes to assert call ordering,
  durable-before-ACK, exact stored XML reuse, and no-resubmit behavior.
- Existing MySQL integration tests remain unchanged and continue to skip
  without `ZATCA_TEST_MYSQL_DSN`/`ZATCA_TEST_DB`.
- Focused `gofmt`, `go test`, and `go vet` are the validation gate.

## Requirement traceability

| Requirement | Design element |
|---|---|
| FR-1/FR-2/FR-3 | Sequence-derived XML ID plus immutable signed XML and separate UUID/ICV fields |
| FR-4 | 2xx response classifier |
| FR-5 | Explicit 409 ambiguous classification |
| FR-6/FR-7/FR-8 | Existing ordered-stream record/complete/release protocol and regression tests |
| FR-9 | Processor and ordered-stream focused tests |
