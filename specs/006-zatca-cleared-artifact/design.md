# Design: T006 Cleared Artifact

## Data flow

```text
provider response
  -> ProviderResult (cleared XML, response/reference)
  -> tenant-scoped artifact writer
  -> submissionOutcome paths/response
  -> T003 submission + attempt + outcome + artifact rows
```

For standard clearance, the returned cleared XML is written as the
`cleared_xml` artifact before the final outcome is completed. For reporting,
the signed request remains the authoritative submitted artifact and provider
response evidence is retained as `response_xml` metadata/artifact where
available.

## Contract changes

- Add response evidence fields to `ProviderResult`, `submissionOutcome`, and
  `artifactRecord` without changing the public daemon entry points.
- Preserve `ProviderRef` and all existing provider warning/rejection fields.
- Extend completion persistence to write the existing response compatibility
  column and insert response artifact metadata.
- Reuse the current `cleared_xml_path` and request path columns; no schema
  migration is introduced.

## Failure handling

- Artifact configuration missing or an artifact directory/file write fails:
  return a wrapped error naming the artifact and path.
- If failure occurs before provider submission, T004 records terminal failure
  through the existing completion path.
- If failure occurs after provider submission, the provider was called and the
  completion attempt records the explicit terminal failure; the processor
  returns the storage error. It never sets legacy state `3` or reports
  success.
- Existing provider transport classification and retry metadata are unchanged.

## Test strategy

- Add a fake clearance result with unique cleared XML and provider reference.
- Assert the exact cleared XML is retained by the fake durable store, the
  `cleared_xml` artifact is present, and the provider reference remains linked.
- Assert an artifact-store configuration/write failure returns an error and
  cannot mark the document issued.
- Keep the existing accepted/warning/rejected/pending/ambiguous/nil T004 cases.

## Requirement traceability

| Requirement | Design element |
|---|---|
| R1 | cleared XML carried through `ProviderResult` to `cleared_xml` artifact/path |
| R2 | response/reference fields and response artifact in completion contract |
| R3 | request artifact and response evidence retained for reporting |
| R4 | fail-closed artifact writer and explicit completion error |
| R5 | unchanged T004 state mapping and legacy update guard |
