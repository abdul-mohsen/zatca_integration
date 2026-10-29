# T008 — Design

## Build boundary

The repository is the default Docker build context. The authorized
`zatca-einvoicing-sdk-238-R4.0.0` directory is supplied as a separate named
context called `zatca-sdk`:

```text
docker build --build-context zatca-sdk=<private SDK root> .
```

The Dockerfile copies the root of that named context into
`/SDK/zatca-einvoicing-sdk-238-R4.0.0`. This keeps all existing SDK paths and
runtime commands unchanged while removing the implicit repository-directory
assumption.

## Dockerfile behavior

1. Keep the Go builder stage unchanged.
2. In the runtime stage, consume the `zatca-sdk` named context rather than an
   unqualified repository `COPY`.
3. Retain the existing `WORKDIR`, `FATOORA_HOME`, `SDK_CONFIG`, `PATH`, config
   rewrite, executable permission, daemon copy, entrypoint, port, and volume.
4. Add a layout check after the copy so an incorrectly selected private SDK
   root fails with a message naming the expected `Apps/fatoora` input.

If the named context is omitted from a direct Docker build, Docker reports the
missing `zatca-sdk` context. The README gives the corrective command. Compose
adds an earlier, actionable `${ZATCA_SDK_PATH:? ...}` interpolation failure.

## Compose behavior

Change only the daemon image build declarations:

```yaml
build:
  context: .
  additional_contexts:
    zatca-sdk: ${ZATCA_SDK_PATH:?ZATCA_SDK_PATH must point to the private ZATCA SDK root}
```

The test-runner image remains independent. No runtime environment variable or
container mount is introduced for the SDK.

## Regression test

Add a root Go test that reads the tracked Dockerfile, Compose files, README, and
`.gitignore` and asserts:

- the Dockerfile uses `COPY --from=zatca-sdk`;
- the old unqualified SDK `COPY` is absent;
- the runtime SDK path and key runtime declarations remain present;
- both daemon Compose files require `ZATCA_SDK_PATH` through
  `additional_contexts`;
- README documents the explicit direct build and missing-input guidance; and
- the SDK remains ignored.

The test is deliberately static so it runs without proprietary files, Docker,
credentials, or network access.

## Security and licensing

The named context is operator-provided and never committed. The repository
does not validate or claim the SDK's license or provenance; release/build
operators must use an authorized artifact. No credentials enter the Docker
build context through this change.

## Requirement traceability

| Requirement | Design element |
|---|---|
| FR-001/FR-002 | `zatca-sdk` named context and `COPY --from=zatca-sdk` |
| FR-003 | Required Compose `ZATCA_SDK_PATH` additional context |
| FR-004 | Copy target and existing runtime declarations unchanged |
| FR-005 | Static Docker/Compose/README contract test |
| FR-006 | README and `.env.example` build instructions |
| NFR-001 | Explicit, separately supplied SDK input |
| NFR-002 | No SDK or credentials tracked |
| NFR-003 | Runtime stages and Go code unchanged |
| NFR-004 | Red/green test and repository validation sequence |
