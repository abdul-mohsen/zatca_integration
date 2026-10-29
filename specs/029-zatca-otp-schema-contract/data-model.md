# T029 Data Model

## `branch_zatca_config`

The existing backend table stores branch CSR inputs and ZATCA credentials. T029 does not add,
remove, or rename any active field.

| Field | T029 relationship |
|---|---|
| `branch_id` | Existing branch key used by credential persistence. |
| Active CSR and credential fields | Persisted exactly as before. |
| `zatca_otp` | Legacy field outside the daemon's active write contract; no runtime write is allowed. |

## Invariants

- Successful onboarding continues to persist the same active credential fields.
- The daemon does not require the presence of `zatca_otp`.
- No onboarding state transition is changed by T029.
