# T011 Implementation Plan

1. Establish the T010/T029-compatible durable state store and guarded
   transitions.
2. Add stage callbacks to the existing onboarding processor/provider flow.
3. Integrate the onboarding worker so queue/failure/done persistence controls
   acknowledgement and post-success worker activation.
4. Add fake regression coverage, preserve no-OTP SQL, update README, format,
   run targeted/full tests, converge, review, and commit.

Runtime dependency: tenant databases must have T010 migration
`0009_zatca_onboarding_state.sql`; no daemon migration is included.

