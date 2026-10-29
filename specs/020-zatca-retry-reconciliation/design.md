# T020 Design

## Boundary

```text
queue message
    -> worker retry classifier
        -> processor.ProcessBill / existing note entry points
            -> SubmissionProvider / client APIError normalization
            -> existing T015 reconciliation gate
            -> existing T004 durable submission store
```

The worker remains responsible for bounded retry and queue acknowledgement.
The processor remains responsible for classifying provider/client failures and
persisting the durable outcome for each attempt. No public runtime entry point
or queue subject changes.

## Failure taxonomy

`ProviderError` gains an explicit retry decision while retaining its existing
T003 outcome state:

| Failure | State | Retry | Durable next retry |
|---|---|---:|---:|
| accepted / accepted with warning | success | no | null |
| ordinary provider 4xx | rejected | no | null |
| HTTP 408, 409, 425, 429 | ambiguous | yes | bounded backoff |
| HTTP 5xx | ambiguous | yes | bounded backoff |
| timeout / transport uncertainty | ambiguous | yes | bounded backoff |
| malformed signed/request/cleared XML | terminal_failure | no | null |
| nil/invalid provider result | terminal_failure | no | null |
| transient retry exhausted | terminal_failure | no | null |

Unknown transport errors remain retryable/ambiguous by default so an
unrecognized infrastructure failure is not silently discarded. The worker
uses the typed classification rather than parsing provider messages.

## Durable state and terminal/dead-letter representation

The existing T003/T004 columns remain authoritative:

- `retry_count` and attempt number represent progress;
- `next_retry_at` is set only when another retry is allowed;
- provider/error/rejection fields retain the reason;
- `OutcomeTerminalFailure` represents terminal and exhausted/dead-letter work;
- no new migration or `dead_letter` database state is introduced.

The worker marks the final in-process attempt as dead-letter eligible through
internal message context. The processor converts a retryable failure on that
attempt to `terminal_failure` before `CompleteSubmission`. Permanent failures
already persist `rejected` or `terminal_failure` with a nil next retry. The
worker alerts and ACKs both permanent and exhausted messages after durable
completion.

## Retry flow

1. Normalize the queue message and preserve its stable logical identity.
2. For each attempt, copy incremented attempt metadata into the message
   context without changing the stable key.
3. Call the existing processor entry point.
4. If it succeeds, ACK.
5. If the returned error is non-retryable, alert/dead-letter and ACK without
   another attempt.
6. If it is retryable and the budget remains, sleep using the existing bounded
   exponential delay and call the processor again.
7. On the final retryable attempt, pass terminal/dead-letter context so the
   durable outcome becomes `terminal_failure`, then alert and ACK.

The retry loop is factored around an injectable attempt function so worker
tests can prove the policy without NATS, MySQL, or SDK runtime dependencies.

## Processor changes

- Add a retryability flag to `ProviderError` and helpers that default
  `ambiguous`/`pending` to retryable and `rejected`/`terminal_failure` to
  permanent.
- Update `classifyProviderError` to whitelist only temporary 4xx statuses
  (408/409/425/429); other 4xx statuses are permanent.
- Wrap pre-provider XML/request failures in typed terminal errors so the
  worker does not retry invalid content.
- Validate signed and returned cleared XML as well-formed XML; retain the
  existing artifact and request behavior.
- Make `submissionOutcome` retry-aware so rejected and terminal outcomes have
  no next retry timestamp.
- On the final retryable attempt, persist `terminal_failure` with a bounded
  `RETRY_EXHAUSTED` error code while retaining provider/error context.
- Have the durable start gate short-circuit previously rejected/terminal
  submissions instead of allocating another provider attempt.

## Reconciliation

The existing T015 path remains the only reconciliation path:

1. An ambiguous prior durable attempt returns `needsReconciliation`.
2. The processor reconstructs the same UUID/hash and calls
   `SubmissionReconciler`.
3. Accepted reconciliation completes the existing attempt without `Submit`.
4. Definitive not-found allocates a new attempt with the same identity.
5. Unsupported or ambiguous lookup records an ambiguous outcome and never
   blindly submits.

The production adapter continues to return
`RECONCILIATION_UNSUPPORTED`; no provider endpoint is invented.

## Test strategy

- Processor tests cover HTTP status classification, invalid XML terminal
  handling, retry timestamp behavior, terminal exhaustion, and terminal
  duplicate gating with fake provider/store seams.
- Worker tests use the injectable attempt function to prove one attempt for a
  permanent error, bounded retries for a transient error, incremented message
  attempt metadata, and final dead-letter context.
- Existing T015 timeout-after-acceptance tests remain and are extended or
  preserved to prove one submit plus one reconciliation and no duplicate.
- Client HTTP fake tests retain `APIError` behavior and verify status/body
  preservation; no live provider call is made.

## Requirement traceability

| Requirement | Design element |
|---|---|
| FR-1 | Typed provider/client classification table |
| FR-2 | Existing attempt/retry fields plus retry-aware outcome |
| FR-3 | Terminal state and nil next retry for dead-letter work |
| FR-4 | Injectable worker retry loop and ACK policy |
| FR-5 | Existing T015 reconciliation gate and stable identity |
| FR-6 | Existing provider/store/worker seams and unchanged scope |
| FR-7 | Focused processor, worker, and client fake tests |
