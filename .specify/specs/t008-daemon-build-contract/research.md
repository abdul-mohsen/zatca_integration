# T008 — Research and evidence

## Repository evidence

- `Dockerfile` at base `6021a43` uses
  `COPY zatca-einvoicing-sdk-238-R4.0.0 /SDK/zatca-einvoicing-sdk-238-R4.0.0`.
- `.gitignore` ignores `zatca-einvoicing-sdk-*/`, so the Dockerfile's source is
  absent from a normal checkout by design.
- `sdk/sdk.go` writes credentials and invokes `fatoora` using the absolute
  runtime path `/SDK/zatca-einvoicing-sdk-238-R4.0.0`; that path must remain
  unchanged.
- `docker-compose.yml` and `docker-compose.test.yml` currently use the
  repository root as the daemon build context and do not declare an SDK input.
- The repository has no tracked SDK directory or credentials.
- The current worktree is clean on `fix/zatca/c1-daemon-build-contract` at
  `6021a43`.

## Authoritative build-tool research

Docker's BuildKit documentation describes named contexts passed with
`docker build --build-context NAME=VALUE` and consumed by Dockerfile
instructions such as `COPY --from=NAME`.

Docker Compose's Build Specification supports `build.additional_contexts` as a
mapping of named contexts to host paths. This allows the repository source and
the private SDK source to remain separate while Compose still builds the same
image.

## Alternatives considered

1. **Keep the SDK directory in the default context.** Rejected: it preserves
   the hidden ignored-directory dependency and risks accidentally expanding the
   repository context with proprietary content.
2. **Require a private SDK archive and checksum.** Strong provenance option,
   but it adds archive format, extraction, checksum distribution, and operator
   workflow that the current repository does not define. It is larger than
   needed for T008.
3. **Use a named SDK build context.** Selected: it is a Docker-native explicit
   input, keeps the existing runtime path, works for both direct Docker and
   Compose builds, and does not require SDK bytes in the repository.

## Decision

Use the named context `zatca-sdk` for the private SDK root. The Dockerfile will
copy from that context into the unchanged runtime directory and validate the
expected SDK layout. Direct Docker instructions will show the required
`--build-context zatca-sdk=...` flag. Both Compose daemon services will require
`ZATCA_SDK_PATH` and map it to the same named context. The build-contract test
will statically enforce these declarations and runtime invariants.

## Validation constraints

The Docker client is installed, but the local Docker daemon is unavailable
(`docker version` cannot connect to the Linux engine). Therefore the Docker
image build can only be inspected statically in this worktree; Go tests remain
available. The proprietary SDK's provenance and licensing status were not
provided and remain an operator/release blocker for a real image build.
