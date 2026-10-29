# Chain State Contract

The daemon's processor boundary requires a chain state store with these
operations:

```text
Reserve(branch_id, doc_type, document_id, proposed_uuid, proposed_issue_at)
  -> position { pih, icv, uuid, issue_at, prepared payload, status }

SavePrepared(position, signed_xml, invoice_hash)

CommitAccepted(position, accepted_hash, invoice_uuid, qr)
```

`Reserve` is idempotent by `(branch_id, doc_type, document_id)`. `CommitAccepted`
is idempotent for the same accepted hash and must atomically update document
metadata, `previous_invoice_hash`, `last_icv`, and reservation status.
Missing schema/state is an error. The contract does not permit a fixed PIH or
ICV fallback.
