# Requirements: PR33 D10 Identity and Recovery

## Problem

Reviewer findings #2 and #5 for PR `zatca_integration #33` need a contract-level
answer in the ZATCA daemon:

1. The document identity printed on an invoice and carried in XML must not be
   confused with the branch's fiscal counter, the database row identifier, or
   the UUID used for provider idempotency.
2. Provider outcomes and queue recovery must remain safe when the process
   crashes, a delivery is redelivered, the provider returns an ambiguous
   conflict, or a successful HTTP response contains validation errors.

The current change must be limited to these two findings. Evidence that requires
an actual ZATCA sandbox remains external evidence and must not be fabricated.

## Goals

- Preserve one stable, contract-correct document identity across the printed
  invoice, generated XML, and durable submission record.
- Keep the fiscal counter/previous-hash chain, document identity, and UUID
  distinct and auditable.
- Make provider acceptance, warning, rejection, technical failure, crash, and
  ambiguous conflict behavior deterministic and redelivery-safe.
- Add focused local tests for the corrected behavior.

## Non-goals

- No changes to onboarding, credit/debit-note processing, tax calculations, or
  unrelated queue behavior.
- No provider sandbox calls or claims about sandbox acceptance.
- No GitHub writes, pull-request updates, pushes, or schema changes outside this
  worktree.
- No redesign of the provider API client beyond what is required to classify the
  stated response cases.

## User scenarios and acceptance

### Scenario 1: Create a document with a branch identity

Given a final bill with a branch/printed sequence value, when the daemon builds
the signed invoice, the XML invoice ID is the same business document identity
that users see on the bill. The database row ID must not silently replace that
identity. The UUID and fiscal ICV remain separate fields.

### Scenario 2: Re-deliver after a pre-submit crash

Given a signed invoice committed locally but no provider outcome recorded, when
the delivery is retried, the daemon submits the exact stored signed XML and does
not rebuild the invoice, change its identity, or allocate another ICV.

### Scenario 3: Provider accepts or warns

Given a successful provider response with no validation errors, or with warning
messages only, the daemon records the corresponding accepted outcome before
acknowledging the delivery. A later redelivery does not submit the invoice again.

### Scenario 4: Successful HTTP response contains validation errors

Given a 2xx provider response containing validation errors, the daemon records a
rejection (including provider code/message) rather than treating HTTP success as
fiscal acceptance. The bill is not marked issued.

### Scenario 5: Crash or technical failure around provider submission

Given a transport/timeout/server failure, the daemon leaves the delivery
retryable and preserves the committed invoice for a later attempt. A process
crash between provider acceptance and local outcome persistence may result in an
ambiguous re-delivery response; the daemon must classify a 409 as ambiguous and
surface reconciliation rather than silently marking accepted or sending
unbounded duplicates.

### Scenario 6: Ambiguous 409

Given a 409 response for an already-held invoice, the daemon records an
ambiguous outcome, does not mark the bill issued, acknowledges the delivery only
after durable recording, and emits an operator-reconciliation signal. A later
redelivery does not submit the recorded invoice again.

## Functional requirements

- **FR-1 Document identity:** The generated XML `Invoice` ID must use the
  contract's branch/printed document identity, not the database primary key or
  the fiscal ICV. If the source record has no usable identity, processing must
  fail as a rejected input rather than inventing a substitute.
- **FR-2 Identity persistence:** The durable submission and source-record
  updates must retain enough information to prove which XML document identity,
  UUID, ICV, and hash were committed together. Existing signed XML must remain
  immutable for retries.
- **FR-3 Booking separation:** The ICV and previous invoice hash must continue
  to come from the branch chain booking; they must not be derived from the
  printed identity or database row ID.
- **FR-4 Outcome classification:** Classify 2xx responses with validation errors
  as rejected, warning-only responses as accepted-with-warning, and clean
  successful responses as accepted.
- **FR-5 Ambiguous conflict:** Classify HTTP 409 responses as ambiguous, retain
  provider details, and never convert them into a rejection or acceptance
  without authoritative reconciliation.
- **FR-6 Durable-before-ack:** Accepted, warning, rejected, and ambiguous
  outcomes must be recorded before the queue delivery is acknowledged.
- **FR-7 Redelivery safety:** A committed invoice is retried from stored signed
  XML; a recorded outcome makes the request terminal and prevents another
  provider submission.
- **FR-8 Technical retry:** Technical failures before outcome recording release
  the claim/leave the delivery unacknowledged, while failures that cannot be
  retried as input errors are completed and reported.
- **FR-9 Focused tests:** Tests must cover identity separation, provider
  classification, pre-submit crash/re-delivery, accepted-outcome no-resubmit,
  ambiguous 409, and 2xx validation errors.

## Non-functional requirements

- Changes must be minimal and compatible with the existing tenant/branch
  processing contract.
- No secrets, credentials, or provider responses from an external sandbox may be
  committed.
- Focused `go test`, `go vet`, and `gofmt` checks must be runnable locally.

## Assumptions

- `bill.sequence_number` is the source of the branch/printed invoice identity
  for bills handled by this branch.
- `zatca_submission.signed_xml` is the immutable source of the XML retried
  after a crash, while the source bill retains its printed sequence.
- The current repository owns the daemon code and tests but not the backend's
  schema migrations.
- A provider 409 does not prove acceptance; only an authoritative reconciliation
  query or provider evidence can resolve it.

## Risks and unknowns

- The exact ZATCA sandbox response for duplicate UUIDs and the exact printed
  identity formatting require external environment evidence and are unresolved
  locally.
- Existing production rows may have an empty sequence value. The implementation
  must choose a contract-safe failure or an explicitly documented compatibility
  path rather than silently changing identity.
- The backend schema may not have a dedicated stored XML document-ID column; if
  so, the signed XML plus source sequence must remain the evidence instead of
  inventing a migration in this repository.

## Success measures

- Focused tests demonstrate that XML identity is not the database row ID or ICV,
  and that stored/retried invoices preserve the same signed payload.
- Focused tests demonstrate all four provider cases (clean 2xx, warning-only
  2xx, validation-error 2xx, and 409) and the durable-before-ack/re-delivery
  behavior.
- `gofmt`, targeted `go test`, and targeted `go vet` complete successfully.
- Final report explicitly separates locally verified behavior from unresolved
  sandbox evidence and lists any follow-up issue recommendations.
