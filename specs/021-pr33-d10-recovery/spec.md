# PR33 D10 Identity and Recovery

## Summary

Keep the printed/branch document identity, the ZATCA UUID, and the fiscal
counter distinct while making provider outcomes safe across crashes and
redelivery.

## Scope

This feature addresses only reviewer findings #2 and #5 for PR
`zatca_integration #33`:

- D10 XML invoice ID versus branch sequence/printed identity and stored
  records.
- Provider acceptance, crash/redelivery, ambiguous 409, and 2xx validation
  error semantics and tests.

Sandbox evidence, unrelated document types, and unrelated queue changes are
out of scope.

## User stories

### P1 — Document identity

1. As an operator, I need the XML invoice ID and printed invoice identity to
   refer to the same document, while the UUID and ICV remain independently
   traceable.

### P1 — Recovery

2. As an operator, I need a process crash or provider ambiguity to leave a
   durable, reconcilable result rather than silently accepting or duplicating
   an invoice.

### P2 — Local proof

3. As a maintainer, I need focused tests that demonstrate the outcome and
   redelivery contract without requiring a live provider.

## Functional requirements

- The XML root invoice ID is the non-empty branch/printed sequence from the
  source bill.
- A missing sequence is rejected before booking an ICV.
- The database primary key, UUID, and ICV are never used as a substitute XML
  invoice ID.
- The immutable signed XML remains the retry payload, and the existing
  submission/source records preserve the UUID, ICV, hash, and printed
  sequence relationship.
- A clean 2xx is accepted; warning-only 2xx is accepted with warning; a 2xx
  containing validation/provider errors is rejected.
- A 409 is ambiguous, not accepted or rejected, and requires reconciliation.
- Outcomes are recorded before ACK. A recorded outcome is terminal for
  redelivery; a technical failure remains retryable.

## Acceptance criteria

- [x] A focused builder test proves XML ID comes from `sequence_number` and
      differs from both the bill row ID and booked ICV.
- [x] A focused validation test proves a missing sequence cannot book/build a
      document.
- [x] Focused provider tests cover clean 2xx, warning-only 2xx, 2xx validation
      errors (including explicit error/status signals), and 409 ambiguity.
- [x] Focused stream tests cover stored-XML redelivery after a submit failure,
      provider acceptance followed by local record failure and ambiguous
      re-delivery, and no resubmit after an outcome is recorded.
- [x] `gofmt`, targeted `go test`, and targeted `go vet` pass or are recorded
      as blockers.
- [x] The final report distinguishes local evidence from external sandbox
      evidence and recommends follow-up issues where reconciliation needs
      provider support.

## Constraints and assumptions

- Use the existing tenant schema contract; do not invent a new schema column.
- `bill.sequence_number` is the source of the printed identity.
- No live credentials or provider responses are available for this change.
