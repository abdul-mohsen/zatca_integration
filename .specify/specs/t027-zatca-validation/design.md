# T027 Design: Strict ZATCA Taxpayer and CSR Validation

## Boundary flow

```text
Taxpayer / DB onboarding data
        |
        v
Taxpayer.Validate
        |
        v
Taxpayer.Config -> Config.Validate -> CSRConfig.Validate
        |                              |
        |                              +--> VAT / required CSR fields /
        |                                   country / invoice map /
        |                                   four-letter + four-digit location
        v
SDK.GenerateCSR / Service.OnboardWithCSR / provider client
```

Every path that can start CSR generation or onboarding provider traffic has a
local validation gate. Invalid input returns an error before subprocess or HTTP
work.

## Components and responsibilities

### `config`

- Own the reusable field-format predicates and error messages.
- Validate taxpayer required fields and Saudi VAT syntax.
- Validate the complete CSR configuration contract, including location.
- Keep default generation (`Country`, `InvoiceType`, TIN, UUID) unchanged.

### `processor`

- Keep the existing load and onboarding state flow unchanged.
- Run strict taxpayer/CSR validation after loading branch data and before
  constructing or invoking the ZATCA service.

### `sdk` and `zatca`

- Apply the CSR/config preflight at direct SDK and service onboarding entry
  points so callers that bypass the processor cannot send malformed data.
- Do not add provider calls, retries, persistence, or state transitions.

## Validation rules

| Field | Rule |
|---|---|
| VAT / Organization Identifier | 15 ASCII digits, first and last digit `3` |
| Required strings | Non-empty after whitespace check |
| Country | Two uppercase ASCII letters (ISO Alpha-2 representation) |
| Invoice type | Four ASCII digits, each `0` or `1` |
| CSR location | Four ASCII letters followed by four ASCII digits |

`Taxpayer` optional defaults remain responsible for filling `Country` and
`InvoiceType` before CSR validation. The short address has no safe default.

## Error handling

- Return field-specific errors suitable for wrapping with branch context.
- Do not include OTPs, private keys, certificates, or API credentials.
- Preserve existing error wrapping at processor/service boundaries.

## Persistence and migration

No schema, migration, query, onboarding state, or credential persistence change
is part of T027.

## Test strategy

- Config unit tests cover valid fixtures, malformed VAT digits/boundaries,
  whitespace-only required fields, malformed short addresses, and valid CSR
  fields.
- Onboarding-focused tests cover the preflight behavior using in-memory
  taxpayer/branch data and assert invalid input is rejected before the provider
  boundary.
- Existing package tests and `go test ./...` provide regression coverage.

## Requirement traceability

| Requirement | Design element |
|---|---|
| FR-001 | Shared VAT validator used by taxpayer and CSR validation |
| FR-002 | Required-string checks in taxpayer and CSR validation |
| FR-003 | Short-address validator on taxpayer location and CSR location |
| FR-004 | Processor, service, and SDK preflight gates |
| FR-005 | Existing valid fixtures retained in tests |
| FR-006 | Field-specific errors with no secret values |

