# T020 Implementation Plan

1. Add red focused tests for provider status classification and worker retry
   decisions, including final terminal/dead-letter context.
2. Extend processor/provider error classification and typed terminal XML
   failures while preserving the existing provider/client seams.
3. Make durable outcomes retry-aware, terminal on exhausted retry, and
   short-circuit prior permanent/terminal submissions.
4. Refactor the worker retry loop around an injectable attempt function,
   increment attempt metadata, and stop retrying permanent failures.
5. Preserve and verify the existing T015 timeout reconciliation path; add
   focused assertions for the unsupported production lookup limitation.
6. Update README and T020 status/spec artifacts, then run format, targeted
   tests, full tests, vet, and diff review.
7. Create one isolated local commit with no push or PR.

## Constraints

- No PIH/ICV, XML mapping, UI, migration, or public queue subject changes.
- Use only existing provider/client and durable submission seams.
- Keep all changes in the T020 worktree and commit.
