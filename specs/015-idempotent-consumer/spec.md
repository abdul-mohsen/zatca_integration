# Specification: Idempotent ZATCA Consumer

T015 makes the daemon safe for live/replay and redelivered events by combining
the T014 stable event key with T004 durable submission identity and a provider
reconciliation gate for ambiguous outcomes.

Success requires:

1. T014 and legacy messages decode to one tenant/document identity.
2. Replay publication uses the same stable JetStream identity as live
   publication.
3. Durable accepted results short-circuit duplicates.
4. Ambiguous results are reconciled before a new provider request.
5. Timeout-after-acceptance proves one submit and one successful lookup.
6. T020 retry/dead-letter and T017 PIH/ICV remain unchanged.

The current provider lookup and dedicated stable-key schema limitations are
explicit blockers, not silent fallbacks.
