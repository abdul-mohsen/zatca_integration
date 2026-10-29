# T030 design

## Boundary

`invoice.ImportService` is a side-effect-free application service. Its
`Import(context.Context, ImportRequest)` method accepts bytes and provenance
metadata, delegates XML parsing and signature/schema checks to the existing
`ParseSignedUBL` boundary, and returns `ImportResult` containing:

- a defensive copy of the original bytes;
- source evidence/hash with request metadata;
- the canonical versioned `UBLImport`;
- explicit validation/failure evidence.

The context is accepted for caller cancellation and future adapters; the
current parser is synchronous and performs no I/O.

## Failure model

`ImportFailureClass` values are stable strings:

- `malformed`
- `unsafe`
- `oversized`
- `required_fields`
- `signature`
- `trust_unavailable`
- `schema`
- `unsupported`

`ImportFailure` contains the class, message, wrapped cause, validation issues,
and (when parsing completed) the `UBLImport` evidence. Sentinel errors allow
`errors.Is`; `errors.As` exposes the class and parsed evidence. The service
returns a result for review-gated unsupported/trust-unavailable outcomes when
the XML is safely parsed, while hard parser failures return a classified error.

## Data flow

```text
ImportRequest
  └─ defensive byte copy + metadata
      └─ ParseSignedUBL(bytes, parser options)
          ├─ XML safety / UBL root / normalized mapping
          ├─ signature and certificate evidence
          ├─ required-field and optional schema issues
          └─ unknown/unsupported raw XML evidence
      └─ classify policy-relevant status
          └─ ImportResult or *ImportFailure
```

No database, object store, network, NATS, posting, or UI dependency is
introduced.

## Policy

- Safe valid XML with unsupported fields is returned with
  `FailureClassUnsupported` evidence; unsupported raw XML stays in the
  canonical result.
- Missing required fiscal fields is a hard `required_fields` failure because
  the result is not safe for downstream intake, but the parsed result is
  attached to the error.
- Invalid/tampered signature is a hard `signature` failure.
- A structurally signed document whose certificate parses but has no supplied
  roots is a `trust_unavailable` failure, never `verified`.
- Schema validator errors become `schema` failures; warning-only results stay
  usable with warning evidence.

## Test strategy

- Test the service API, not only parser internals.
- Use repository fixtures and in-memory generated certificates; no secrets or
  provider files.
- Assert original bytes are unchanged and copied, source metadata is retained,
  canonical fields are deterministic, failure class is stable, and unsupported
  raw evidence is preserved.
