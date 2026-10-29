# T029 Validation Guide

## Prerequisites

- Go 1.25 or newer.
- Repository checked out at the T029 worktree.

## Red Regression

With the baseline cleanup still present, run:

```powershell
rtk go test ./processor -run TestOnboardSQLDoesNotWriteLegacyOTP -count=1
```

Expected result: the test fails and identifies the executable `zatca_otp` update.

## Green Validation

After removing the obsolete cleanup:

```powershell
gofmt -w processor/onboard_schema_test.go
rtk go test ./processor -run TestOnboardSQLDoesNotWriteLegacyOTP -count=1
rtk go test ./processor
rtk go test ./...
```

Expected result: all commands pass, and the focused contract reports no executable onboarding
write to `zatca_otp`.
