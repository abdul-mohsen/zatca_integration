# T029 Design

## Boundary

The implementation is limited to the successful onboarding persistence boundary:

1. ZATCA onboarding completes.
2. Credentials are written using the existing active columns.
3. No second write is attempted against the legacy OTP column.

No state-machine, API, encryption, or retry behavior changes.

## Source Changes

- Delete the obsolete post-success `UPDATE ... zatca_otp` call from
  `processor/onboard.go`.
- Retain the existing credential `INSERT` and all error handling around it.
- Replace the migration script's instruction to issue the legacy cleanup with documentation
  that OTP cleanup is intentionally not performed by the daemon.

## Contract Test

Add `processor/onboard_schema_test.go`. The test parses `processor/onboard.go` with the Go AST,
finds executable calls whose method is `Exec`, extracts their SQL string arguments, and fails if
any such query references `zatca_otp`. This is intentionally an offline schema contract:

- It fails on the current baseline because the cleanup update is present.
- It passes after the cleanup is removed.
- It ignores explanatory comments and does not require a live database.

## Failure Handling and Compatibility

The credential save remains the final database operation in the success path. Existing errors
from loading, CSR generation, ZATCA calls, encryption, and credential persistence remain
unchanged. Removing the best-effort cleanup means old schemas with a retained OTP column simply
leave that legacy value untouched; the daemon no longer owns or persists the single-use OTP.

## Traceability

| Requirement | Design element |
|---|---|
| FR-001 | Remove the legacy update; AST contract rejects any `zatca_otp` write. |
| FR-002 | Keep the existing credential upsert unchanged. |
| FR-003 | Offline `processor` schema-contract test. |
| FR-004 | Update migration documentation section 5. |
| NFR testability | No external services or schema mutation required. |

## Validation

Run the focused contract test red before the source fix, then green after the fix. Run `gofmt`
on changed Go files, the targeted processor test, and `go test ./...`.
