# T011 Design

## Components

### Onboarding state store

Add a daemon-local SQL state store backed by the tenant's `adminDB` (falling
back to `db`). It will:

- refresh/record queued state and resolve the current attempt;
- atomically update branch and attempt progress;
- atomically record failed state/error/retry evidence;
- atomically finalize done state and completion timestamps.

All non-queue updates are guarded by `branch_id` and `attempt_number`. The SQL
uses the T010 field names and does not mention `zatca_otp`.

### Worker acknowledgement boundary

The onboarding worker will:

1. resolve/refresh queued state;
2. run onboarding with stage progress callbacks;
3. on error, persist failed state before alerting/ACKing;
4. on state persistence failure, leave the NATS message unacknowledged so
   JetStream can redeliver it;
5. on success, persist done state, then spawn branch document workers, then ACK.

Malformed/expired messages retain their existing safe-drop behavior; this lane
does not alter generic document processing.

### Processor/service progress

`processor.OnboardBranch` remains a compatibility wrapper. A progress-aware
variant reports CSR before generation and delegates to a progress-aware
`zatca.Service` method. The service reports compliance, invoices, and
production immediately before those existing provider stages. Callback errors
abort onboarding and are returned as durable-state errors.

### Error normalization

Normalize provider failures to bounded non-secret code/message pairs:

- invalid OTP for explicit OTP/auth rejection;
- provider unavailable for HTTP 5xx, network, or timeout failures;
- onboarding error for other failures.

No retry loop is added. A failed state is retry-eligible through T010's
backend transition; the next accepted request gets a new attempt.

## Data flow

```text
NATS onboard message
        |
        v
Queue/resolve current (branch, attempt)
        |
        v
CSR -> compliance -> invoices -> production callbacks
        |       |          |          |
        +-------+----------+----------+
                branch + attempt guarded writes
                         |
              success -> done -> spawn -> ACK
              failure -> failed -> ACK
                         |
              state-write error -> no ACK
```

## SQL transition rules

### Queued

Refresh `onboard_state`, `onboard_step`, `onboard_queued_at`, and
`last_attempt_at`. Do not increment a backend-created attempt. Legacy rows in
`not_started`/`failed` may be queued only when a current attempt is absent; the
normal T010 path remains authoritative for new attempt creation.

### Progress

Update branch state/step/retry/error and `last_error`, set
`onboard_started_at=COALESCE(onboard_started_at,NOW())`, and refresh
`last_attempt_at`. Update the matching history row's state/step/retry/error and
`started_at=COALESCE(started_at,NOW())` in the same transaction.

### Failed

Update branch to `failed`, `failed`, increment `onboard_retry_count`, store
bounded error code/message in both error fields, set
`onboard_started_at` if needed, and refresh `last_attempt_at`. Update history
to failed, increment history retry count, store evidence, and set
`completed_at`. Commit both or return an error.

### Done

Update branch to `done`, `done`, clear current error fields, set completion
timestamps and `zatca_onboarded_at` if absent, and refresh last attempt.
Update history to done and set `completed_at`. Commit both before activation.

## Test strategy

- Use a fake state store and fake onboarding runner to test worker decision
  ordering without a live provider or database.
- Table-drive invalid OTP and provider outage failure classification and
  acknowledgement behavior.
- Test successful done-before-activation behavior.
- Test a second request after failure receives a later attempt and stale error
  evidence is cleared by queue preparation.
- Keep a source-level assertion that onboarding SQL has no `zatca_otp`.
- Run targeted packages, `gofmt`, and the full repository suite.

## Runtime and schema dependency

The daemon binary depends at runtime on T010 migration `0009` being applied to
each tenant database and on the backend publishing a T010-compatible queued
attempt. No daemon migration is added. Without the columns/table, state writes
fail and the message remains unacknowledged rather than falsely disappearing.

## Requirement traceability

| Requirement | Design element |
|---|---|
| FR-1 | Queue/resolve state store and attempt guard |
| FR-2 | Processor/service progress callbacks |
| FR-3 | Atomic failed branch/history writes and no-ACK-on-write-error |
| FR-4 | Atomic done writes before branch worker activation |
| FR-5 | No OTP SQL and unchanged document worker path |
| FR-6 | Fake worker regression suite and source compatibility test |

