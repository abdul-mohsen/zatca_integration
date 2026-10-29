# T025 Implementation Plan

1. Record T025 requirements, research evidence, design, tasks, and status.
2. Add focused red debit builder/query/persistence regression tests.
3. Correct debit original-document identity and carry accepted XML artifact
   persistence through the existing chain store.
4. Verify debit queue/replay paths against the T024 source contract without
   changing retry behavior.
5. Run gofmt, targeted tests, full tests, vet, convergence, and self-review.
6. Commit only the T025 daemon/spec changes; do not push or create a PR.
