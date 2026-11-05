# Requirements: ZATCA Chain Owner and Reservation Contract

## Problem

The backend removed `pkg/egschain`, but its Goose `00005_egs_chain_reservation.sql`
still creates the ICV reservation columns. The ZATCA daemon must make the
runtime ownership boundary explicit and prove that its SQL path remains safe
under concurrent booking, release, commit, and redelivery.

## Goals

- Establish the ZATCA daemon as the single runtime owner of the branch ICV/hash
  chain and reservation lifecycle.
- Prove that the daemon actually uses the four reservation columns and the
  committed chain columns.
- Add database-backed concurrent booking/release/commit tests and a
  database-backed redelivery test.
- Document the backend schema contract without changing the backend worktree.

## Non-goals

- Do not modify the backend repository or its Goose migrations.
- Do not add a second chain implementation or change the reservation protocol.
- Do not change invoice/provider behavior unrelated to chain ownership.
- Do not push or create GitHub writes.

## Functional requirements

- **FR-1 Runtime owner:** `queue.SQLChain` is the only runtime implementation
  used by the daemon for booking, releasing, committing, and advancing
  `last_icv`/`previous_invoice_hash`.
- **FR-2 Reservation contract:** Booking reads and writes
  `icv_reservation_key`, `icv_reservation_token`, `reserved_icv`, and
  `reservation_lease_expires_at`; release and commit clear only the owned
  reservation.
- **FR-3 Concurrency:** Concurrent requests for one branch have at most one
  live booking; a released booking can be taken by the other request without
  reusing a committed ICV.
- **FR-4 Atomic commit:** Commit stores the signed submission, source bill
  metadata, chain advancement, and reservation cleanup in one transaction.
- **FR-5 Redelivery:** A committed-but-unanswered invoice is loaded from
  `zatca_submission.signed_xml` on redelivery; it is not rebuilt or assigned a
  new ICV, and a recorded outcome makes the request terminal.
- **FR-6 Documentation:** ZATCA documentation states that backend migrations
  own schema definitions while the daemon owns runtime chain values, and lists
  the required columns/tables and fresh/existing tenant assumptions.
- **FR-7 Backend handoff:** The exact contract and any required backend action
  are reported to the backend agent; no backend files are edited here.

## Acceptance criteria

- [ ] Code inspection identifies the daemon's `SQLChain` construction and every
  reservation/chain SQL operation.
- [ ] A concurrent booking test proves one owner, reservation ownership, safe
  release, and successful takeover/commit.
- [ ] A redelivery test using `SQLChain`/`SQLClaimStore` proves exact stored
  invoice reuse and no further provider call after completion.
- [ ] Existing commit and crash/lease tests remain passing.
- [ ] README and the backend-agent handoff clearly state the ownership boundary.
- [ ] Focused Go tests, vet, and gofmt pass; MySQL-gated tests are explicitly
  reported if no test database is available.

## Assumptions and risks

- Backend Goose `00001`, `00003`, `00005`, and `00010` remain the schema contract
  for tenant databases.
- The backend application no longer needs `pkg/egschain`, but removing
  `00005`'s columns would break the current daemon SQL path.
- Database-backed concurrency/redelivery tests require the configured MySQL
  test environment and may be skipped locally.
