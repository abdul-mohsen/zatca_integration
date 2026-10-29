# T029 Implementation Plan

## Approach

1. Add the executable-SQL schema contract test while the baseline cleanup is still present.
2. Run the focused test and record the expected red result.
3. Remove only the obsolete onboarding cleanup statement.
4. Update the migration note so it no longer prescribes the removed write.
5. Format and run focused/full Go validation.
6. Review the diff for T029-only scope, run convergence/self-review checks, and commit.

## Files

- `processor/onboard_schema_test.go` — new regression/schema contract.
- `processor/onboard.go` — remove the legacy cleanup write.
- `scripts/migration_zatca_schema.sql` — remove stale cleanup instruction.
- `specs/029-zatca-otp-schema-contract/*` — requirements, research, design, tasks, status,
  and quality artifacts.

## Decisions

- Do not add a deprecated `zatca_otp` column.
- Do not alter onboarding state or credential fields.
- Do not convert the cleanup into another write; the active flow does not need OTP persistence.
- Do not change the historical test fixture because the runtime contract is the requested
  boundary and the fixture is not the active backend schema source.

## Validation Gates

- Red: `go test ./processor -run TestOnboardSQLDoesNotWriteLegacyOTP -count=1`
- Green: same focused command after the fix.
- Format: `gofmt -w processor/onboard_schema_test.go` then `gofmt -d` on changed Go files.
- Targeted/full: `go test ./processor` and `go test ./...`.
- Final: diff/status review and isolated commit; no push or PR.
