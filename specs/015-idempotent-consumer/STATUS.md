# T015 Status

## Completed

- T015A: requirements created before source changes.
- T015B: research and design decisions recorded, including T014 read-only
  envelope alignment and T003/T004 schema/provider limitations.
- T015C-RED: focused queue and processor regressions fail on the current
  `670be54` code. The queue tests report missing T014 envelope fields and
  normalization helpers; the processor tests report missing reconciliation
  interfaces and durable-state flags.
- T015D: T014-compatible message normalization and stable replay
  `Nats-Msg-Id` support implemented, while retaining legacy aliases.
- T015E/T015F: durable accepted/in-flight/ambiguous state gating and worker
  message-context wiring implemented without mutating cached branch config.
- T015G: optional provider reconciliation and same-identity not-found retry
  implemented; unsupported production lookup remains explicitly ambiguous.
- T015H: duplicate, timeout-after-acceptance, identity-reuse, envelope, and
  worker-context focused tests pass.
- T015I: README and T015 artifacts document behavior and limitations.
- T015J: final formatting, targeted tests, full `go test ./...`, vet, and
  diff review completed; isolated commit `c5e4f33` created locally.

## In Progress

- None.

## Blocked

- Production timeout reconciliation is blocked by the current client having
  no provider query endpoint. The implementation must surface
  `RECONCILIATION_UNSUPPORTED` and avoid a blind second request.
- T003 does not have dedicated stable-key/outbox/attempt columns. T015 will
  persist the provider UUID in `document_id` and event correlation in request
  artifact JSON metadata.
- Credit/debit durable persistence remains blocked by T004's bill-keyed
  submission schema.

## Backlog

- Add a provider lookup/query endpoint and response reference mapping.
- Add backend durable columns or monitor support for stable event key/outbox
  correlation if required by operations.
- Implement T017 PIH/ICV and T020 retry/dead-letter in their dedicated lanes.

## Risks

- A stale pending row is recoverable after the five-minute bounded lease,
  while newer pending work is treated as an in-flight duplicate.
- A changed invoice hash for the same provider UUID must not be silently
  reused after an ambiguous outcome.

## Next Actions

1. No further T015 action; do not push or create a pull request.

## Validation Evidence

- Red queue command at `670be54`: failed to compile because the daemon lacked
  T014 envelope fields and normalization helpers.
- Red processor command at `670be54`: failed to compile because durable
  attempt-state flags and reconciliation interfaces were absent.
- Focused green commands:
  `go test ./queue -run TestMessage -count=1`,
  `go test ./processor -run TestDuplicateDeliveryShortCircuitsAfterAcceptance -count=1`,
  `go test ./processor -run TestTimeoutAfterAcceptanceReconcilesBeforeResubmitting -count=1`,
  `go test ./processor -run TestNotFoundReusesProviderIdentityBeforeResubmitting -count=1`,
  and `go test ./cmd/daemon -run TestConfigForMessageCopiesStableEnvelopeContext -count=1`.
- Targeted suites: `go test ./processor ./queue ./cmd/daemon -count=1` passed.
- Formatting: `gofmt -l` over all changed Go files produced no output.
- Full suite: `go test ./... -count=1` passed.
- Vet: `go vet ./...` passed.
