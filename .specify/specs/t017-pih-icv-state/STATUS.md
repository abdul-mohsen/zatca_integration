# T017 Status

## Completed

- Requirements and repository diagnosis recorded.
- Research, design, plan, and dependency-ordered tasks created.
- Baseline confirmed clean at `6021a43` on
  `fix/zatca/h3-pih-icv-ordering-v2`.
- Focused red tests added for zero ICV rejection, removal of fixed builder
  state, shared branch positions, branch isolation, and retry-safe commits.
- Red run confirmed the expected gap: `invoice` accepts zero ICV and
  `processor` has no chain-state implementation.

## In Progress

- T017-6: final self-review and isolated commit.

## Blocked

- Production rollout is blocked until the separate backend T016 integration
  applies the equivalent `zatca_chain_position` reservation contract and
  provisions branch PIH/ICV state.

## Backlog

- Operational migration of old per-document durable NATS consumers to the
  mixed-document branch consumers.

## Risks

- The backend repository may expose different reservation column names than
  the repository-local contract.
- Existing persistent NATS data may contain old per-document durable consumers;
  rollout should remove or migrate those consumers operationally.
- Race testing could not run because the environment has CGO disabled; the
  deterministic concurrent memory-store test and full suite pass.

## Next Actions

- Run final diff/spec self-review, commit the isolated T017 changes, and report
  the backend/runtime integration limitation without pushing.

## Completed Validation

- Red: `rtk go test ./invoice ./processor` failed before implementation as
  expected (zero ICV accepted and chain store undefined).
- Green: `rtk go test ./invoice ./processor ./queue ./cmd/daemon`.
- Green: `rtk go test ./...` (63 tests/packages reported passing).
- Green: `rtk go vet ./...`.
- Green: `rtk git diff --check`.
- `rtk go test -race` was attempted but the environment requires
  `CGO_ENABLED=1`; it is not a T017 implementation failure.

## Final Integration Note

The daemon now fails closed when `branch_zatca_config.previous_invoice_hash`,
`last_icv`, or `zatca_chain_position` is unavailable. The backend T016
repository at `b602c05` must provide the equivalent atomic per-branch/EGS
reservation and PIH ownership contract. No backend source was edited here.
