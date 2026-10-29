# T029 Status

## Completed

- T029-01: requirements, specification, research, design, plan, task graph, and checklist
  created.
- Baseline confirmed clean at `6021a43` on `fix/zatca/m3-schema-drift`.
- Repository history and migration evidence confirm the remaining issue is the separate
  post-success legacy cleanup.
- T029-02: AST-based executable-SQL contract added in `processor/onboard_schema_test.go`.
- T029-03: legacy cleanup removed from `processor/onboard.go` and stale migration instruction
  replaced in `scripts/migration_zatca_schema.sql`.
- T029-04 red/green gate: the focused contract failed before the fix and passes after it.
- T029-04 validation: `gofmt`, `go test ./processor`, and `go test ./...` pass.
- README review complete: onboarding instructions and user-visible workflow are unchanged, so
  no README update applies.
- Convergence self-review: requirements, design, tasks, implementation, and validation are
  aligned; no additional convergence tasks are required.
- T029-04 complete: isolated commit created on `fix/zatca/m3-schema-drift`; no push or pull
  request created.

## In Progress

- None.

## Blocked

- None.

## Backlog

- None after the current validation wave.

## Risks

- `scripts/test_setup.sql` retains a historical `zatca_otp` column, but it is not the active
  backend schema source and is intentionally outside this runtime cleanup patch.
- Full tests may expose pre-existing environment/dependency issues; record them without widening
  scope.

## Next Actions

None; T029 is complete.
