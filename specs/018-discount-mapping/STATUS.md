# T018 Status

## Completed

- Requirements, research, design, plan, tasks, and checklist artifacts created.
- Prerequisite `11c0435` confirmed as the clean base.
- Focused discounted-invoice fixture regression added and run red before the
  production behavior fix.
- Discount row mapping and exact XML total reconciliation implemented.
- Gofmt, targeted tests, and full Go suite completed successfully.
- Convergence and self-review completed; no migration is required.

Validation evidence:

- Red: `rtk go -C C:\ssda\chatGPT\worktrees\zatca-fix-t018-zatca test ./processor -run TestBuildBillInvoiceDiscountedXMLFixture -count=1`
  failed before the behavior fix with `invoice discount = 0; want 10`.
- Green: the same focused command passed after the fix.
- Targeted green: `rtk go -C C:\ssda\chatGPT\worktrees\zatca-fix-t018-zatca test ./invoice ./processor -count=1`.
- Full green: `rtk go -C C:\ssda\chatGPT\worktrees\zatca-fix-t018-zatca test ./... -count=1`.
- Static green: `rtk go -C C:\ssda\chatGPT\worktrees\zatca-fix-t018-zatca vet ./...`.
- Formatting: `rtk gofmt -w` on all changed Go files; `git diff --check` passed.

## In Progress

- None.

## Blocked

- Authorized ZATCA sandbox/provider validation is unavailable in this lane.

## Backlog

- None for T018. Provider/runtime evidence belongs to the later release gate.

## Risks

- Provider-specific validation cannot be claimed without the proprietary SDK
  and authorized sandbox.
- Existing public invoice numeric fields remain float-shaped for compatibility;
  internal amount arithmetic is exact and XML output is two-decimal.

## Next Actions

- Commit the isolated worktree changes with the required co-author trailer.
- Do not push or create a pull request.
