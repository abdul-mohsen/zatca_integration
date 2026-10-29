# T011 Research

## Evidence reviewed

### T010 backend contract (`zatca-fix-t010-backend`, commit `922335c`)

Read-only inspection of `pkg/db/migrations/0009_zatca_onboarding_state.sql`,
`pkg/db/queries/branch.sql`, generated `pkg/db/gen/branch.sql.go`, the handler,
and backend README established:

- Current-row fields are `onboard_state`, `onboard_step`, `onboard_attempt`,
  `onboard_retry_count`, `onboard_error_code`, `onboard_error`,
  `onboard_queued_at`, `onboard_started_at`, and `onboard_completed_at`.
- Compatibility fields `last_error`, `last_attempt_at`, and
  `zatca_onboarded_at` remain part of the write contract.
- History is `zatca_onboarding_attempt`, keyed by
  `(branch_id, attempt_number)`, with `state`, `step`, `retry_count`,
  `error_code`, `error_message`, `queued_at`, `started_at`, and
  `completed_at`.
- Backend queueing creates the attempt and publishes the existing message,
  which has no attempt number. The daemon must read the current attempt and
  guard every update with branch plus attempt.
- T010 `CompleteZatcaOnboarding` and failure/progress queries are the source
  of truth for state names and compatibility updates.

Decision: use the current branch attempt from the tenant database and do not
change the queue payload in T011.

### Existing daemon onboarding flow

`cmd/daemon/worker.go` has a tenant-wide onboarding consumer. It deduplicates
fresh OTP messages, calls `processMessage`, sends an alert on error, and ACKs
both success and failure. `processor.OnboardBranch` generates the CSR, persists
the CSR/key, then calls `zatca.Service.OnboardWithCSR`, which internally has
compliance CSID, six compliance invoices, and production CSID boundaries.

Decision: add a write-capable onboarding state store around the existing
worker, and add progress callbacks at the existing processor/service stage
boundaries. Invoice worker code remains untouched.

### T029 compatibility (`zatca-fix-t029-zatca`, commit `e78bdd9`)

The T029 diff removes the best-effort
`UPDATE branch_zatca_config SET zatca_otp = ''` cleanup and its comment. The
credential INSERT already omits `zatca_otp`.

Decision: remove/retain no SQL reference to `zatca_otp`; add a source regression
test if needed so future onboarding changes cannot reintroduce it.

### Error behavior

`client.APIError` carries an HTTP status and body and is wrapped by the client
and service. `errors.As` can conservatively identify invalid OTP responses and
provider 5xx responses. Network/timeout failures are provider-unavailable
class errors. Error text is bounded before persistence.

Decision: classify only clear invalid-OTP/provider-unavailable cases and use a
generic onboarding code otherwise; persist the wrapped error without OTP input.

## Alternatives considered

1. **Add attempt number to NATS payload** — rejected because T010 publishes the
   existing payload and changing the backend protocol is outside T011.
2. **Reuse generic `processWithRetry`** — rejected because onboarding OTPs are
   single-use and the request explicitly excludes generic invoice retry work.
3. **ACK every failure after logging** — rejected because it loses durable
   recovery evidence and violates FR-3.
4. **Update only `branch_zatca_config`** — rejected because T010 defines an
   append-only attempt history and daemon updates must keep both views aligned.
5. **Persist OTP for replay** — rejected for security and T029 compatibility.

## Remaining uncertainty

No blocking unknowns remain. Live migration execution depends on a MySQL
environment and is a runtime dependency reported in the final result.

