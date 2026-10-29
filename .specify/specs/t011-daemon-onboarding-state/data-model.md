# T011 Data Model

## Current branch metadata (`branch_zatca_config`)

| Field | Meaning | Daemon write rule |
|---|---|---|
| `onboard_state` | Current operational state | `queued`, `compliance`, `production`, `done`, or `failed`; legacy `csr`/`invoices` remain readable |
| `onboard_step` | Fine-grained worker step | `queued`, `csr`, `compliance`, `invoices`, `production`, `done`, `failed` |
| `onboard_attempt` | Monotonic backend attempt number | Read from the current row; every update is guarded by it |
| `onboard_retry_count` | Failure/retry evidence | Reset by backend queue; incremented on durable failure |
| `onboard_error_code` | Bounded machine-readable failure | Cleared on queue/done; set on failure |
| `onboard_error` | Bounded human-readable failure | Cleared on queue/done; set on failure |
| `onboard_queued_at` | Queue timestamp | Refreshed when daemon accepts message |
| `onboard_started_at` | First processing timestamp | Set once at first progress/failure |
| `onboard_completed_at` | Terminal timestamp | Set on done or failed |
| `last_error` | Legacy-compatible error | Mirrors current failure; cleared on queue/done |
| `last_attempt_at` | Last daemon transition timestamp | Refreshed on every transition |
| `zatca_onboarded_at` | Legacy-compatible completion timestamp | Set if absent on done |

## Attempt history (`zatca_onboarding_attempt`)

One row is identified by `(branch_id, attempt_number)`. It records the
state/step, retry count, error code/message, and queued/started/completed
timestamps. The daemon updates the matching row in the same transaction as the
current branch row.

## State transitions

```text
queued/queued
  -> queued/csr
  -> compliance/compliance
  -> compliance/invoices
  -> production/production
  -> done/done

any active step -> failed/failed
failed -> queued (only after a later backend retry request)
```

The OTP is not an entity in this model and is never persisted.

