# Implementation Plan: T004

1. Establish the base regression test and capture red evidence.
2. Add a small processor provider interface and production adapter around the
   existing SDK/client; classify response/error outcomes and generate stable
   document UUIDs.
3. Add a transactional SQL store using the exact T003 submission,
   attempt, outcome, and artifact names. Return explicit errors for missing
   schema/privileges and unsupported note documents.
4. Integrate bill processing so pending is recorded before provider work,
   final outcomes/artifacts are durable, and legacy state is set to `3` only
   for accepted outcomes.
5. Add fake-provider regression assertions for accepted, warning, rejected,
   timeout/ambiguous, and nil results.
6. Update README/status, run gofmt, targeted tests, full tests, diff review,
   convergence, and commit one isolated PR-ready change.

## Key decisions

- Preserve existing `ProcessBill`/worker signatures.
- Use numeric master tenant ID for T003 `merchant_id` and string tenant ID for
  filesystem scoping.
- Do not silently fall back to legacy-only writes.
- Do not reuse the T003 bill submission row for credit/debit notes.
