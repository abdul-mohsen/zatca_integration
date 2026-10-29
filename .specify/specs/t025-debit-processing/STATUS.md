# T025 Status

## Completed

- T025-1: Requirements, research, design, official spec/plan, and task graph
  created.
- Baseline branch confirmed clean at `a2b4c06`.
- T024 backend reference `98ae215` reviewed; it supplies durable debit source
  identity, output columns, and generic submission source context.
- T025-2: Focused debit builder/query regressions added; red run failed before
  implementation.
- T025-3: Debit source identity, original bill reference, discount/line mapping,
  T024 source-context queries, and accepted XML-path persistence implemented.
- T025-4: Existing branch worker/replay routing verified; no retry or subject
  redesign added.

## In Progress

- T025-6: Final diff self-review and isolated commit.

## Blocked

- Production runtime is blocked until backend T024 migrations 0011/0012 and
  the T017 chain-state contract are applied to each tenant database.

## Backlog

- None within T025. Retry redesign and UI integration remain out of scope.

## Risks

- A legacy tenant with only the pre-T024 debit table will return a schema
  error until migrations are applied.
- XML artifact persistence depends on the configured writable XML directory.

## Next Actions

- Create the isolated T025 commit without pushing, then report the backend
  migration/runtime dependency.

## Completed Validation

- Red: `rtk go test ./processor -run 'TestBuildDebitNoteInvoice' -count=1`
  failed before implementation on source identity and original reference.
- Green: `rtk go test ./processor ./queue ./cmd/daemon -count=1`.
- Green: `rtk go test ./... -count=1` (67 tests across 20 packages).
- Green: `rtk go vet ./...`.
- Green: `rtk gofmt -l` on all changed Go files produced no output.
- Green: `rtk git diff --check`.
