# T015 Plan

1. Add red queue and processor fake regressions.
2. Add T014-compatible message normalization and stable replay message IDs.
3. Add tenant/document-scoped durable state lookup and duplicate short-circuit
   around the T004 SQL store.
4. Carry stable message context into deterministic provider UUID generation.
5. Add optional provider reconciliation before any retry submission, preserving
   identity after a not-found result and blocking unsupported lookups.
6. Run targeted red/green tests, then formatting, full tests, vet, convergence,
   self-review, and one isolated local commit.
