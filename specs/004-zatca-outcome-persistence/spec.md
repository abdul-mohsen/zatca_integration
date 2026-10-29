# Specification: ZATCA Daemon Outcome Persistence

## User stories

1. An operator can distinguish accepted, accepted-with-warning, rejected,
   pending, ambiguous, and terminal ZATCA outcomes for a submitted bill.
2. An operator can inspect the provider code/warning, attempt number, stable
   UUID/hash/QR, and request/cleared artifact paths for the same tenant.
3. A non-success provider result never makes the invoice appear issued.
4. A processor test can exercise provider outcomes without SDK binaries,
   credentials, or live ZATCA access.

## Functional scope

- Add a replaceable provider boundary behind existing daemon APIs.
- Persist T003 submission, attempt, outcome, and artifact fields.
- Preserve tenant context and existing queue/runtime APIs.
- Leave note processing explicitly blocked where T003 cannot represent it
  safely.

## Success criteria

| ID | Criterion |
|---|---|
| SC-001 | Five focused fake-provider cases (accepted, warning, rejected, timeout, nil) are red at `6021a43` and green after T004. |
| SC-002 | Accepted and warning cases set legacy state `3`; rejected, pending, ambiguous, terminal, and nil cases do not. |
| SC-003 | Every completed bill attempt has an explicit T003 outcome and attempt record, with provider fields retained when present. |
| SC-004 | UUID/hash/QR/request/cleared evidence remains associated with the same tenant and stable document identity across retries. |
| SC-005 | Targeted tests and `go test ./...` pass, or exact schema/grant blockers are documented. |

## Constraints

- Base commit is `6021a43`; backend prerequisite is read-only `fc084a4`.
- No backend worktree edits, push, PR, credentials, or live provider claim.
- Existing processor and runtime entry points remain available.
