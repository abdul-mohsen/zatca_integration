# T030 — Callable signed-UBL import service

## Problem

The repository has invoice generation and signing primitives, but supplier UBL
input is not exposed as a callable import service. A caller must be able to
submit the exact XML bytes to one boundary that retains evidence, parses the
supported UBL fields, and reports trust and validation outcomes without
silently treating a parsed document as trusted or ready for posting.

## Goals

- Expose a synchronous, in-process import service over the existing signed-UBL
  parser boundary.
- Preserve the exact input bytes and source/provenance metadata in the result.
- Return deterministic canonical parsed invoice data and parser evidence.
- Validate XML safety, required fiscal fields, signature shape/cryptographic
  status, optional schema validation, and supplied trust material.
- Make failure classes explicit and machine-checkable.
- Keep this lane independent of database persistence, posting, workers, and UI.

## In scope

- A callable Go service in the existing `invoice` package.
- Request metadata for MIME type, original name, content reference, and
  provenance.
- Exact-byte retention in the returned result plus content hash.
- Reuse of the existing `ParseSignedUBL`/`ParseUBL` parser contract.
- Required-field, unsupported-field, schema, signature, and trust outcomes.
- Focused valid, tampered, and unsupported UBL tests.

## Out of scope

- Database or object-store persistence.
- HTTP/NATS/CLI adapters.
- Automatic posting, inventory, accounting, or duplicate-record persistence.
- Obtaining or embedding ZATCA production trust roots.
- Frontend or review UI.
- Claiming a fixture is cryptographically valid without authorized trust
  material.

## User stories

1. As an intake caller, I can import UBL bytes and retrieve the original bytes,
   provenance, canonical fields, hash, and validation evidence together.
2. As a validation caller, I can distinguish malformed/unsafe input, required
   field failures, tampered/invalid signatures, unavailable trust, schema
   failures, and unsupported content.
3. As a downstream reviewer, I can see unsupported fields and signature evidence
   without the service mutating or discarding the supplier XML.

## Functional requirements

- **FR-1:** The service accepts a request containing non-empty UBL bytes and
  source metadata, and returns a result or typed/classified failure.
- **FR-2:** The result retains a defensive copy of the exact original bytes,
  their SHA-256 content hash, byte size, MIME type, original name, content
  reference, and provenance.
- **FR-3:** The service delegates UBL parsing to the existing parser boundary
  and returns its versioned canonical `UBLImport` data.
- **FR-4:** XML safety and size limits are enforced before mapping; malformed,
  unsafe, and oversized input have distinct failure classes.
- **FR-5:** Required fiscal fields are validated and missing/invalid fields are
  reported as a required-fields failure, while preserving parsed evidence when
  possible.
- **FR-6:** Signature presence, digest/signature status, and certificate
  evidence remain separate from trust. A valid certificate without configured
  trusted roots is not reported as verified.
- **FR-7:** Tampered or cryptographically invalid signed input is reported as a
  signature failure; unsupported/unknown UBL fields remain visible and are
  reported as unsupported content rather than discarded.
- **FR-8:** Optional schema validation and trust material are supplied through
  existing parser options; absence of provider material is an explicit,
  documented limitation.
- **FR-9:** The service performs no persistence or side effects.

## Non-functional requirements

- Deterministic output for identical bytes/options.
- No external entity, DTD, network, or local-resource resolution.
- No mutation of caller-owned input bytes.
- Public API is package-testable without a live provider, database, or secret.
- Error classification supports `errors.Is`/typed inspection.

## Acceptance criteria

- Red tests fail before the service boundary exists because the callable API
  cannot be resolved.
- Green tests cover:
  - a valid supported fixture retaining bytes/provenance and canonical fields;
  - tampered/invalid signed input classified as signature failure and never
    verified;
  - unsupported UBL content classified explicitly while raw evidence remains.
- `gofmt` is clean and targeted tests plus `go test ./...` pass.
- No persistence/frontend files are changed.
- A commit contains only T030 changes and no push/PR is created.

## Assumptions, constraints, risks, and unknowns

- The existing parser implementation on the repository's signed-UBL feature
  line is the authoritative mapping/safety baseline and will be brought into
  this branch without changing unrelated code.
- No authorized ZATCA certificate chain or authentic signed supplier fixture is
  available in this lane; trust verification can only be proven with injected
  test roots or reported as unavailable.
- A schema validator is an injected dependency because no XSD/provider package
  is present in the base repository.
- “Canonical parsed data” means the existing versioned `UBLImport` normalized
  contract, not re-serialized XML or a posting model.
- Unsupported fields must not make the original XML disappear; callers may
  choose a review policy later.
