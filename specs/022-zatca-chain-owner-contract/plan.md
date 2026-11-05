# Plan: ZATCA Chain Owner and Reservation Contract

## Phase 0 — Research

- Trace daemon construction and SQLChain SQL statements.
- Inspect the backend migration contract read-only.
- Send the ownership boundary to the backend agent.

## Phase 1 — Implementation

1. Add/update real-MySQL concurrent booking, release, commit, and redelivery
   tests.
2. Update test helpers only as needed to use fixture bill IDs.
3. Document runtime/schema ownership and fresh/existing tenant assumptions in
   ZATCA documentation/comments.

## Phase 2 — Validation

- Run focused queue tests and full Go tests where practical.
- Run focused/full vet and gofmt.
- Verify no backend files changed and commit only ZATCA changes.
