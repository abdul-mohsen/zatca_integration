# T020 Status

## Completed

- T020A: Requirements and repository research created.
- T020B: Design, plan, task graph, and status tracking created.
- T020C: Focused red tests added. Baseline worker test compilation fails
  because `processWithRetryFunc` and final dead-letter context do not exist;
  baseline provider tests fail because `IsRetryableError` does not exist.
- T020D: Provider/client failures now distinguish permanent 4xx and invalid
  XML from transient timeout/transport/server failures.
- T020E: Worker retries only classified transient failures, preserves bounded
  backoff, carries local retry context, and stops permanent failures.
- T020F: Retry metadata is durable through T003/T004 fields; exhausted
  transient work becomes `terminal_failure` with `RETRY_EXHAUSTED`, and
  permanent/terminal durable submissions are gated from resubmission.
- T020G: Timeout-after-acceptance and provider lookup limitation tests pass;
  no blind resubmission is introduced.
- T020H: README and all T020 artifacts updated; convergence review found no
  remaining specified work.
- T020I: `gofmt -l` is clean; targeted processor/worker/client/queue tests,
  `go test ./...` (85 tests across 20 packages), and `go vet ./...` pass;
  `git diff --check` is clean.

## In Progress

- Isolated commit and final self-review.

## Blocked

- Live timeout reconciliation is blocked by the current ZATCA client lacking
  a lookup-by-UUID/invoice-hash endpoint. The production adapter must report
  `RECONCILIATION_UNSUPPORTED`; fake-provider tests cover the positive path.

## Backlog

- None for T020 scope.

## Risks

- Provider-specific 4xx semantics may differ; only 408/409/425/429 are
  explicitly treated as transient.
- T003 has no separate dead-letter state, so terminal failure/error fields
  must remain the durable representation.
- Full runtime validation may be limited by unavailable ZATCA credentials,
  SDK runtime, MySQL, or provider lookup.

## Next Actions

- Remove generated test artifacts, review the final diff for T020-only scope,
  commit with the required trailer, and do not push or open a PR.
