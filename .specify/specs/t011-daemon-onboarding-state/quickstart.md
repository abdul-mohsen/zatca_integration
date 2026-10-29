# T011 Quickstart Validation

## Prerequisites

- Go 1.25 or newer.
- The target worktree is on `fix/zatca/c5-onboarding-state-daemon`.
- Tenant databases have backend migration
  `pkg/db/migrations/0009_zatca_onboarding_state.sql` from T010.

## Focused regression tests

From `C:\ssda\chatGPT\worktrees\zatca-fix-t011-zatca`:

```powershell
rtk go test ./cmd/daemon -run 'TestOnboard'
rtk go test ./processor -run 'TestOnboardSQL'
```

Expected outcomes:

- Invalid OTP and provider outage persist failed evidence before ACK.
- A failed-state persistence error leaves the message unacknowledged.
- Success persists done before activating branch workers.
- A later retry uses a later attempt and completes successfully.
- Onboarding SQL contains no legacy `zatca_otp` reference.

## Repository validation

```powershell
rtk gofmt -w cmd/daemon/*.go processor/*.go zatca/*.go
rtk go test ./cmd/daemon ./processor ./zatca
rtk go test ./...
```

No live provider call is required for the fake regression suite. Live daemon
operation requires the T010 schema and a write-capable `adminDB` connection.

