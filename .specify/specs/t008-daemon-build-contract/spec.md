# T008 — Feature specification

## Summary

Make the daemon Docker/SDK build input explicit without adding the proprietary
ZATCA SDK or changing daemon runtime paths.

## Required behavior

- The repository build and the private SDK source are separate build inputs.
- Docker and Compose builds identify the private SDK input explicitly.
- Missing input is reported with corrective guidance.
- `/SDK/zatca-einvoicing-sdk-238-R4.0.0` and all daemon runtime behavior remain
  unchanged.
- A static regression test protects the contract without requiring the SDK.

## Success measures

1. The pre-change build-contract test is red.
2. The post-change build-contract test is green.
3. `gofmt` and `go test ./...` pass.
4. No proprietary SDK or credentials are tracked.
5. Docker build syntax and Compose declarations can be inspected even when the
   local Docker engine is unavailable.
