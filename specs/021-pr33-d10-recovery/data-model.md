# Data Model: PR33 D10 Identity and Recovery

## Bill source record

- `id`: database row locator; not the XML invoice ID.
- `sequence_number`: branch/printed document identity; required for new
  processing and copied to XML `cbc:ID`.
- `invoice_uuid`: provider idempotency identity populated at commit.
- `invoice_hash`, `qr_code`: committed invoice evidence.
- `state`: source record issuance state; only accepted outcomes mark it issued.

## Signed submission

- `bill_id`, `branch_id`: source/partition relationship.
- `invoice_icv`, `previous_invoice_hash`: fiscal chain evidence.
- `invoice_uuid`: provider identity.
- `invoice_hash`, `invoice_kind`: signed document metadata.
- `signed_xml`: immutable retry payload containing the XML document ID.
- `outcome_state`: pending, accepted, accepted-with-warning, rejected, or
  ambiguous.

## Queue claim

- `(pos_id, bill_request_id)`: request identity.
- `request_fingerprint`: payload identity.
- `state` and lease: processing/completed recovery state.

## Invariants

1. `sequence_number`/XML `cbc:ID`, UUID, ICV, and database `id` are distinct
   concepts.
2. A committed signed submission is not rebuilt on redelivery.
3. A non-pending outcome is terminal for provider submission.
4. Ambiguous is not accepted and does not issue the bill.
