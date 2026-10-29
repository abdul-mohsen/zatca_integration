# T027 Requirements: Strict ZATCA Taxpayer and CSR Validation

## Problem

The daemon currently accepts taxpayer data that can pass a length or non-empty
check while violating the ZATCA CSR input contract. Invalid VAT identifiers and
malformed branch locations can therefore reach the SDK or a ZATCA provider
call, where the resulting failure is late and less actionable.

## Goal

Reject invalid taxpayer and CSR configuration locally, before any SDK process or
ZATCA provider request is attempted.

## Scope

### In scope

- Saudi VAT registration number syntax and digit requirements.
- Required taxpayer and CSR fields.
- ZATCA short-address location syntax used for the CSR location.
- Configuration and daemon onboarding validation boundaries.
- Focused regression tests for invalid acceptance and valid inputs.
- Documentation of the onboarding input contract.

### Out of scope

- Onboarding state-machine changes.
- Database schema changes or schema cleanup.
- OTP validation or queue publication behavior owned by another lane.
- Changes to invoice processing, provider response mapping, or SDK artifacts.

## User stories

1. As an operator onboarding a branch, I receive a local validation error for
   malformed taxpayer data before the SDK is invoked.
2. As an operator configuring an EGS, I can use a valid Saudi VAT number and
   Saudi National Address short address in CSR input.
3. As a maintainer, I can rely on the same CSR requirements at direct SDK and
   service onboarding boundaries.

## Functional requirements

- **FR-001 — VAT format:** A taxpayer/CSR VAT identifier MUST contain exactly
  15 ASCII decimal digits, begin with `3`, and end with `3`.
- **FR-002 — Required fields:** Every required taxpayer field MUST reject both
  empty and whitespace-only input. Every required CSR field MUST be present
  before CSR generation or onboarding provider calls.
- **FR-003 — Short address:** A CSR location MUST be a Saudi National Address
  short address consisting of exactly four letters followed by exactly four
  decimal digits. Other eight-character strings MUST be rejected.
- **FR-004 — Boundary ordering:** Invalid taxpayer/CSR data MUST be rejected
  before SDK subprocess execution and before ZATCA HTTP provider requests.
- **FR-005 — Compatibility:** Valid existing taxpayer fixtures, including
  `310175397400003` and `RRRD2929`, MUST remain accepted.
- **FR-006 — Error clarity:** Validation errors MUST identify the invalid
  field/constraint without exposing credentials.

## Non-functional requirements

- Keep the change isolated to T027 validation/config/onboarding code and tests.
- Preserve the onboarding state machine and database schema behavior.
- Keep validation deterministic and independent of network, Docker, and MySQL.
- Preserve existing Go formatting and package conventions.

## Acceptance criteria

1. A 15-character VAT string containing non-digits fails validation.
2. A 15-digit VAT string that does not start and end with `3` fails validation.
3. An eight-character location that is not four letters plus four digits fails
   validation.
4. Empty and whitespace-only required fields fail validation.
5. Valid taxpayer/CSR fixtures pass validation.
6. Onboarding rejects malformed data before the SDK/provider path.
7. Focused regression tests fail on commit `6021a43` and pass after the fix.
8. `gofmt`, targeted tests, and `go test ./...` pass.
9. No onboarding state-machine or schema cleanup files are changed.

## Assumptions, risks, and unknowns

### Assumptions

- The frozen ZATCA onboarding reference is authoritative for the VAT and CSR
  rules used by this lane.
- The Saudi National Address short-address representation is the format
  intended for `csr.location.address`.

### Risks

- Overly permissive normalization could allow a value that the ZATCA SDK
  rejects; validation therefore does not silently rewrite user input.
- Existing external callers may construct CSR configuration directly rather
  than through `Taxpayer`.

### Resolved unknowns

- The VAT boundary is not merely length: the official onboarding reference
  requires 15 digits starting and ending with `3`.
- The location requirement is not merely non-empty: the Saudi National Address
  short address is four letters followed by four numbers.

