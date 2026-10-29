# Feature Specification: ZATCA OTP Schema Contract

**Feature Branch**: `fix/zatca/m3-schema-drift`  
**Created**: 2026-10-29  
**Status**: Draft for implementation  
**Task**: T029

## User Scenarios & Testing

### Scenario 1: Complete branch onboarding

**Given** a branch has valid onboarding data and the ZATCA flow succeeds  
**When** the daemon persists the resulting credentials  
**Then** it does not issue a write against the legacy `zatca_otp` column  
**And** onboarding completes without an unknown-column warning.

### Scenario 2: Prevent schema drift regression

**Given** the active `branch_zatca_config` contract does not include `zatca_otp`  
**When** a developer changes onboarding SQL  
**Then** the focused contract test fails if executable onboarding SQL writes `zatca_otp`.

## Functional Requirements

- **FR-001**: Onboarding MUST not write to `branch_zatca_config.zatca_otp`.
- **FR-002**: Credential persistence MUST remain unchanged for all active credential fields.
- **FR-003**: The repository MUST include an offline contract test for FR-001.
- **FR-004**: Migration documentation MUST describe the active schema without prescribing the
  removed cleanup update.

## Non-Functional Requirements

- The change is limited to T029.
- The regression test is deterministic and does not need MySQL, NATS, Docker, or ZATCA.
- Existing Go formatting and test conventions remain in force.

## Success Criteria

1. The new focused contract test fails before the implementation change and passes afterward.
2. No executable onboarding SQL contains a write to `zatca_otp`.
3. Targeted processor tests and the complete Go test suite pass.
4. No onboarding state or credential-flow behavior outside the obsolete cleanup changes.

## Key Entities

- `branch_zatca_config`: backend table holding branch CSR and ZATCA credentials.
- `zatca_otp`: legacy column no longer part of the active daemon write contract.
- Onboarding credential save: the existing post-ZATCA persistence operation.

## Assumptions

- The backend, rather than the daemon's historical test fixture, defines the active schema.
- Removing the obsolete cleanup is preferable to reintroducing a deprecated column solely for
  daemon compatibility.
