# T025 Specification

Implement durable debit-note processing in the ZATCA daemon after backend
T024 creates `debit_note` sources. Debit messages and startup replay must
query the debit source, build type-383 XML against the original bill,
reserve the shared branch PIH/ICV position, sign and submit through the
existing pipeline, and persist accepted debit outcome and signed-artifact
metadata on the debit source. Existing retry, queue subject, and UI behavior
remain unchanged.

The backend T024 schema and the T017 chain-state contract are required for
runtime operation; the daemon must fail explicitly when they are absent.
