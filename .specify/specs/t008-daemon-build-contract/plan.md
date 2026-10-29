# T008 — Implementation plan

1. Add a static root Go test for the Docker/Compose/README build contract.
2. Run only that test before changing production Docker or Compose files and
   record the expected red result.
3. Replace the Dockerfile's implicit SDK-directory copy with the explicit
   `zatca-sdk` named context while retaining the existing runtime destination
   and environment paths.
4. Require the same context from both daemon Compose services through
   `ZATCA_SDK_PATH`.
5. Update `.env.example` and README build instructions, including missing-input
   remediation and the private SDK provenance/licensing limitation.
6. Run the focused test green, gofmt, `go test ./...`, Dockerfile/Compose static
   inspection, and final self-review.
7. Commit only T008 changes; do not push or create a pull request.
