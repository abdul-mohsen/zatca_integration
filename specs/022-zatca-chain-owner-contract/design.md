# Design: ZATCA Chain Owner and Reservation Contract

## Runtime ownership

The daemon constructs one `queue.SQLChain` per tenant database. The ordered
stream is the only runtime caller of `Book`, `Release`, `Commit`, `Stored`, and
`Record`. The backend migration set defines the tables and columns but does not
run the booking protocol.

## Reservation lifecycle

```text
Book
  -> branch row FOR UPDATE NOWAIT
  -> reject another live owner
  -> write all four reservation columns
Release
  -> clear only matching key/token
Commit
  -> branch row FOR UPDATE
  -> verify key/token/ICV/PIH
  -> insert signed submission + update bill + advance chain + clear reservation
```

An expired reservation can be taken over with the same next ICV because no
invoice was committed. A committed invoice advances `last_icv` and stores its
signed XML, so redelivery reads the same document instead of rebuilding it.

## Test design

- Add a real-MySQL concurrent booking test to `queue/sql_test.go`. Start two
  requests for one branch, assert one `ErrChainBusy`, inspect the reservation
  owner, release it, prove the other request can book, and commit it.
- Make the ordered-stream fake prepared invoice accept a configurable source
  bill ID, then add a real-MySQL redelivery test using `SQLChain` and
  `SQLClaimStore`. Force a provider failure after commit, redeliver the same
  message, assert the stored invoice was reused once, then assert a completed
  redelivery makes no provider call.
- Keep existing lease, stale-commit, release, ambiguous-outcome, and
  record-before-ACK tests.

## Documentation and handoff

Update the ZATCA README and SQLChain comments with:

- runtime owner: ZATCA daemon;
- schema owner: backend Goose migrations;
- required columns/tables and migration versions;
- fresh/existing tenant assumptions;
- explicit prohibition on deleting `00005` columns without a coordinated
  daemon migration.

Send the same contract to the backend agent; do not edit backend files.

## Rollout and compatibility

No schema or runtime migration is introduced in this ZATCA worktree. Existing
tenants must already have the backend schema contract. A missing reservation
column is a deployment/schema failure and should not be hidden by a fallback
chain implementation.
