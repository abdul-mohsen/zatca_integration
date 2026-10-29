# T029 Requirements: ZATCA OTP Schema Contract

## Problem

The onboarding success path still issues a legacy `UPDATE branch_zatca_config SET
zatca_otp = ''` statement after saving credentials. The active backend schema does not
guarantee a `zatca_otp` column, so a successful onboarding can emit an unknown-column warning
after the credential save has completed.

## Goals

- Make the onboarding success path compatible with the active `branch_zatca_config` schema.
- Remove the unexpected legacy write that can reference `zatca_otp`.
- Add a focused contract test that detects reintroduction of that write.
- Preserve existing onboarding state transitions, credential persistence, and error handling
  unrelated to the legacy cleanup.

## Scope

### In scope

- The post-success cleanup behavior in the onboarding implementation.
- Directly related schema/migration documentation.
- A focused Go schema-contract test covering onboarding SQL.

### Out of scope

- Redesigning onboarding state or retry behavior.
- Adding `zatca_otp` to the active schema.
- Changing credential columns, encryption, API calls, or ZATCA workflow steps.
- Addressing any unrelated findings or warnings.

## User Story

As an operator onboarding a branch, I want a successful onboarding to use the backend's active
schema so that it completes without an unknown-column warning caused by obsolete OTP cleanup.

## Functional Requirements

1. The onboarding success path MUST NOT execute a write referencing the legacy `zatca_otp`
   column.
2. The onboarding success path MUST continue to save the generated credentials and return the
   same success/error results for all unrelated operations.
3. The schema contract test MUST fail if onboarding SQL reintroduces a `zatca_otp` write.
4. Related migration documentation MUST no longer instruct the daemon to issue the legacy
   cleanup update.

## Non-functional Requirements

- The change MUST be limited to T029 and avoid changing onboarding state semantics.
- The contract test MUST run without a live database or ZATCA service.
- Go source MUST be formatted with `gofmt`.
- Existing targeted tests and `go test ./...` MUST remain passing.

## Acceptance Criteria

- [ ] A focused contract test fails against the baseline because the legacy update is present.
- [ ] The legacy update is removed or otherwise no longer targets an absent column.
- [ ] The focused contract test passes after the fix.
- [ ] Related migration documentation matches the active schema contract.
- [ ] `gofmt` reports no required changes.
- [ ] Targeted tests and `go test ./...` pass.
- [ ] The final commit contains only T029 changes, with no push or pull request.

## Assumptions, Constraints, Risks, and Unknowns

### Assumptions

- The active backend schema is authoritative for runtime onboarding and does not require
  `zatca_otp` to be retained or cleared by the daemon.
- The OTP is supplied to onboarding as a single-use message value and does not need to be
  persisted by this cleanup path.

### Constraints

- Work starts at commit `6021a43` in the requested worktree and branch.
- No onboarding state-machine redesign or unrelated cleanup is permitted.
- No live external ZATCA or database dependency is required for the regression test.

### Risks

- A stale test fixture or migration comment could continue to imply that the removed column is
  required, masking future schema drift.
- A broad source scan could accidentally reject harmless documentation; the contract should
  inspect executable onboarding SQL only.

### Unknowns resolved before implementation

- Whether the legacy write is part of credential persistence: repository history shows the
  credential `INSERT` already stopped writing `zatca_otp`; the remaining write is a separate
  best-effort cleanup.
- Whether a migration adds the column: the repository migration adds other onboarding columns
  but does not add `zatca_otp`.
