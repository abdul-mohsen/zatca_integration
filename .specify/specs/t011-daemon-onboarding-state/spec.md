# T011 Daemon Onboarding State

This feature makes asynchronous ZATCA onboarding durable and observable in
the daemon while preserving the existing queue and T029 OTP safety behavior.

## Scope

- Persist T010 onboarding state, step, errors, retries, and timestamps.
- Report CSR, compliance, invoice, and production progress.
- Persist failure before acknowledging; finalize done before activation.
- Cover invalid OTP, provider outage, success, and retry with fake tests.

## Exclusions

Generic invoice outcome/idempotency/retry, outbox redesign, backend/frontend
changes, migrations, and OTP persistence are excluded.

## Acceptance

The requirements and acceptance criteria in `requirements.md` are normative.
The SQL field names and attempt contract in T010 are normative compatibility
requirements.

