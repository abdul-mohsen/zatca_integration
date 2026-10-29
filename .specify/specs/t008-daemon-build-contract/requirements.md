# T008 — Daemon SDK build contract requirements

## Problem

The daemon image Dockerfile assumes that a directory named
`zatca-einvoicing-sdk-238-R4.0.0` exists inside the repository build context. The
directory is intentionally ignored because the ZATCA SDK is proprietary, so a
normal checkout cannot build the daemon image and does not clearly identify the
missing private input.

## Goals

- Make the daemon image's SDK input explicit at build time.
- Keep the proprietary SDK outside version control and outside repository
  credentials.
- Make a missing or misconfigured SDK input fail with an actionable message.
- Preserve the SDK's existing in-image path and daemon runtime behavior.
- Add a focused build-contract regression test that fails against the current
  implicit-directory contract and passes after the change.

## In scope

- The daemon `Dockerfile`.
- Compose build declarations that build the daemon image.
- Build documentation and the environment template.
- A static/build-contract regression test that does not require the proprietary
  SDK.
- T008-scoped Spec Kit artifacts.

## Out of scope

- Modifying the ZATCA SDK, its license, or its source files.
- Committing, downloading, or reproducing proprietary SDK content.
- Changing Go daemon runtime paths, commands, ports, volumes, or credentials.
- Changes to deployment, backend, frontend, or other repositories.
- A full SDK-backed Docker image build when the private artifact is unavailable.

## User stories

1. As a developer with authorized access to the private SDK, I can provide the
   SDK as an explicit build input and build the same daemon image.
2. As a developer without the private SDK, I receive a clear instruction about
   which input is missing instead of relying on an ignored directory.
3. As a maintainer, I have an automated regression check preventing the
   Dockerfile and Compose files from silently reverting to the implicit input.

## Functional requirements

- **FR-001 — Explicit build input:** The daemon image build MUST declare the SDK
  source separately from the repository source context.
- **FR-002 — No implicit repository dependency:** The daemon build MUST NOT
  require a repository-relative ignored directory by an unqualified `COPY`.
- **FR-003 — Compose parity:** Every tracked Compose service that builds the
  daemon image MUST pass the same explicit SDK input and MUST fail early when
  that input is not configured.
- **FR-004 — Runtime compatibility:** The resulting image MUST retain the
  existing `/SDK/zatca-einvoicing-sdk-238-R4.0.0` layout, `FATOORA_HOME`,
  `SDK_CONFIG`, `PATH`, daemon entrypoint, exposed port, and XML volume.
- **FR-005 — Regression coverage:** The focused build-contract test MUST detect
  the old implicit contract and validate the explicit contract without needing
  proprietary SDK files or credentials.
- **FR-006 — Documentation:** Build instructions MUST show how to provide the
  private SDK input and explain the missing-input failure.

## Non-functional requirements

- **NFR-001 — Reproducibility:** A fixed repository revision plus a fixed
  authorized SDK input MUST produce the same build inputs; no hidden checkout
  directory convention may be required.
- **NFR-002 — Security/licensing:** No proprietary SDK bytes, credentials, or
  secrets may be added to the repository.
- **NFR-003 — Minimal change:** Runtime behavior and unrelated packages MUST
  remain unchanged.
- **NFR-004 — Validation:** Run the regression test red before the production
  Docker/Compose change, then green, gofmt, targeted tests, and `go test ./...`.

## Acceptance criteria

- [x] A clean checkout at `6021a43` fails the new build-contract test because
  the SDK input is implicit.
- [x] The final Dockerfile consumes an explicit SDK build input and no longer
  uses the old unqualified SDK-directory `COPY`.
- [x] Both daemon Compose build paths declare and require the SDK input.
- [x] The SDK remains external and ignored; no proprietary files or credentials
  are tracked.
- [x] Runtime SDK paths and Docker behavior listed in FR-004 are unchanged.
- [x] The focused test passes after the production change.
- [x] `gofmt` and `go test ./...` pass, and unavailable Docker/SDK validation
  is explicitly recorded.
- [x] The final commit contains only isolated T008 changes and is not pushed.

## Assumptions, constraints, risks, and unknowns

### Assumptions

- The authorized SDK directory has the existing root layout containing
  `Apps`, `Configuration`, and `Data`.
- Docker BuildKit/buildx and a Compose implementation supporting additional
  contexts are available to image builders.

### Constraints

- The SDK is proprietary and cannot be committed or fetched by this change.
- The working tree is Windows-based, but Docker commands may be run from
  multiple host shells.
- The requested branch is already based on ZATCA main `6021a43`.

### Risks

- A local Docker daemon may be unavailable, preventing an end-to-end image
  build.
- Older Docker Compose versions may not support named additional contexts.

### Unknowns/open questions

- The task does not provide an SDK provenance record, license approval, or
  checksum. This change cannot independently establish those facts.
- The exact authorized SDK filesystem location is deployment-specific and must
  remain an operator-provided input.
