# T017 Data Model

## ChainState

Accepted state for one tenant EGS/branch.

| Field | Rule |
|---|---|
| `branch_id` | Required branch/EGS owner key |
| `previous_invoice_hash` | Required for processing; base64 SHA-256 chain value |
| `last_icv` | Non-negative stored counter; next accepted position is `last_icv + 1` |

## ChainPosition

Durable position owned by one document.

| Field | Rule |
|---|---|
| `branch_id` | Same EGS/branch as `ChainState` |
| `doc_type` | `bill`, `credit`, or `debit` |
| `document_id` | Primary key in the document table |
| `icv` | Positive decimal counter; unique per branch |
| `previous_invoice_hash` | Snapshot of branch accepted PIH at reservation |
| `invoice_uuid` | Stable UUID reused for retries |
| `issue_at` | Stable issue timestamp reused for retries |
| `signed_xml` | Prepared signed payload reused for ambiguous retries |
| `invoice_hash` | Prepared or accepted request hash |
| `status` | `reserved` or `accepted` |
| `accepted_at` | Set only when accepted state commits |

## Relationships and Invariants

- One branch has one accepted chain state.
- One document has at most one position.
- One branch/ICV pair has at most one position.
- A position can be committed only when its PIH equals the current accepted
  PIH and its ICV is exactly the next accepted ICV.
- A failed/rejected submission leaves `ChainState` unchanged.
- Repeating an accepted commit with the same hash is a no-op.
