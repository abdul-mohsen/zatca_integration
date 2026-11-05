# Implementation Plan: PR33 D10 Identity and Recovery

## Phase 0 — Research

- Confirm the source of the printed identity and current XML/storage paths.
- Confirm existing provider and queue recovery behavior.
- Record unresolved sandbox evidence separately.

## Phase 1 — Implementation

1. Normalize and require the bill sequence before booking; use it for XML
   `cbc:ID`.
2. Extend 2xx provider classification for explicit error/status signals while
   preserving existing warning/acceptance behavior.
3. Add focused regression tests for D10 identity and recovery transitions.
4. Update the recovery/identity documentation.

## Phase 2 — Validation

- Format changed Go files.
- Run focused processor, queue, and daemon tests.
- Run focused vet for changed packages.
- Review the diff for scope, schema assumptions, and external-evidence claims.
