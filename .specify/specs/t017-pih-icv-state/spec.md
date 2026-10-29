# T017 Specification

Implement per-EGS/branch PIH and ICV ownership for invoice, credit-note, and
debit-note processing. The daemon must reserve one shared chain position,
render PIH as the stored base64 SHA-256 value and ICV as a decimal counter,
persist the prepared request for retry identity, and advance accepted state
only in an idempotent atomic commit. Missing durable state is an explicit
integration error; fixed PIH/ICV defaults are not allowed.

Out of scope: discounts, party mapping, retry-policy redesign, UI, and backend
source changes.
