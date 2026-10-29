# T030 implementation plan

1. Add the scoped Spec Kit artifacts and record the inherited parser boundary
   and provider/trust limitation.
2. Add focused service tests first; run the targeted package test and capture
   the expected compile failure (red).
3. Bring the existing `invoice` signed-UBL parser and non-proprietary fixtures
   into this branch.
4. Implement `ImportService`, request/result types, defensive byte/provenance
   handling, and explicit failure classification over the parser.
5. Run focused valid/tampered/unsupported tests (green), format changed Go
   files, then run `go test ./...`.
6. Review the diff for scope, update README usage/limitations, mark STATUS and
   tasks complete, and commit without pushing.
