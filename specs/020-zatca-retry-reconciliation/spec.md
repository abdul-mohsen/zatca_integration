# T020: ZATCA Retry Classification and Reconciliation

## Summary

Classify permanent provider/content failures separately from transient
failures, persist bounded retry and terminal/dead-letter outcomes, and
reconcile ambiguous timeout results before resubmitting a stable invoice.

## Scope

In scope: existing daemon worker retry decisions, provider/client error
classification, T003/T004 durable attempt/outcome fields, T015 reconciliation,
and focused fake tests.

Out of scope: PIH/ICV, XML mapping, UI, migrations, public queue subjects, and
live provider lookup endpoints.

## Success Criteria

| ID | Measure |
|---|---|
| SC-001 | Ordinary 4xx and invalid XML failures cause one processing attempt and no next retry timestamp. |
| SC-002 | Transient failures retain increasing attempt/retry metadata and bounded next retry timestamps. |
| SC-003 | Exhausted transient work is durably terminal/dead-lettered and acknowledged without a further retry. |
| SC-004 | Timeout-after-acceptance uses one submit and one reconciliation call, with no duplicate submit. |
| SC-005 | Focused tests, full Go tests, formatting, and vet pass; provider lookup limitations are explicit. |

## Constraints

- Base commit: `d7bbc1a`.
- Existing provider/client and durable submission seams are mandatory.
- One isolated local commit; no push or pull request.
