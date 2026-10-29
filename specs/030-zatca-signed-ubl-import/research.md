# T030 research

## Repository evidence

- Baseline is commit `6021a43` on `fix/zatca/m4-signed-ubl-import`.
- Existing code has UBL generation in `invoice/xml.go`, signing/hash
  primitives in `crypto/`, and no callable supplier import service.
- The repository history contains commit `38a901e` (`feat: add signed UBL
  import boundary`) on `plan/R5/R5-T5-xml-import`. Its `invoice/import.go`
  provides the reusable `ParseUBL`/`ParseSignedUBL` contract, XML safety
  limits, normalized invoice mapping, source hash metadata, unsupported-field
  retention, and optional schema/trust hooks.
- The parser reports `verified`, `unverified`, `unsigned`, and `invalid`
  signature states, but does not itself provide a service request/result
  boundary or typed service failure classes.
- Existing dependencies include `github.com/russellhaering/goxmldsig`,
  `beevik/etree`, and standard-library XML/X.509 support. No provider SDK,
  ZATCA trust bundle, database intake package, or UI package is needed.

## Decisions

1. Reuse the existing parser implementation rather than duplicate UBL
   extraction logic.
2. Add the service in `invoice` so callers use one package boundary and the
   parser's canonical data types remain directly available.
3. Make service failures a typed `ImportFailure` with a stable class and
   `errors.Is` sentinel for each class.
4. Preserve exact bytes in memory only. Persistence is explicitly deferred.
5. Treat schema-unavailable and trust-unavailable as evidence/status outcomes
   unless the caller's policy requires them; a signature that cannot be
   cryptographically verified is never upgraded to `verified`.

## Unresolved provider limitation

No authorized ZATCA trust roots or authentic production signed UBL fixture is
available in the repository. Tests therefore prove invalid/tampered behavior
and trust separation with generated test material only; provider-specific
certificate-chain and authentic ZATCA XPath-transform verification remain
blocked for a later integration lane.
