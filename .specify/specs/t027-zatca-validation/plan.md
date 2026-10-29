# T027 Plan

1. Add red regression tests in config and onboarding-focused tests without
   changing production validation.
2. Add shared strict taxpayer/CSR validation rules from the frozen official
   ZATCA and SPL references.
3. Gate processor onboarding, direct SDK CSR generation, and service onboarding
   before any provider/subprocess call.
4. Update the onboarding input documentation without changing the state machine
   or schema.
5. Run gofmt, targeted tests, `go test ./...`, scope/self-review, and commit
   only T027 changes.

