# T030 status

## Completed

- T030-REQ: scoped requirements, research, design, plan, and dependency graph.
- Repository baseline confirmed clean at `6021a43`.
- T030-RED: `go test ./invoice -run 'TestImportService' -count=1` failed
  because the callable service/types were absent.
- T030-PARSER: existing safe signed-UBL parser boundary and non-proprietary
  fixtures restored under `invoice/`.
- T030-SERVICE: `ImportService`, defensive byte/provenance handling, canonical
  result aliases, and typed failure classes implemented.
- T030-GREEN: valid, tampered, unsupported, and required-field service tests
  pass; tampered/signature and unsupported outcomes retain parsed evidence.
- T030-FULL: `gofmt` and `go test ./... -count=1` pass (65 tests/packages
  reported by the repository runner); README documents the callable boundary.

## In Progress

- None; final self-review is complete and the staged change is ready to commit.

## Blocked

- Authentic ZATCA provider trust roots and signed XPath-transform fixtures are
  unavailable; provider-specific cryptographic verification cannot be claimed.

## Backlog

- Provider-backed authentic signed-UBL verification in a later integration lane.
- Durable source persistence and duplicate controls in the backend lane.

## Risks

- A parsed certificate without configured roots must remain unverified.
- Schema validation is injected because no provider XSD is bundled.

## Next Actions

- Commit the isolated T030 change without pushing or creating a PR.
