# T029 Requirements Quality Checklist: Schema Compatibility

**Purpose**: Unit-test the clarity and completeness of the T029 requirements.
**Created**: 2026-10-29
**Audience**: Reviewer before implementation and commit

> Ownership: `[x]` means the reviewer approved the requirement quality. It does not mean the
> implementation is complete.

## Requirement Completeness

- [ ] CHK001 Are the active `branch_zatca_config` schema boundary and the legacy `zatca_otp` field explicitly distinguished? [Completeness, Spec §Functional Requirements]
- [ ] CHK002 Are credential persistence and onboarding state explicitly protected from unrelated changes? [Completeness, Spec §Non-Functional Requirements]
- [ ] CHK003 Are both the successful onboarding scenario and regression/schema-drift scenario defined? [Coverage, Spec §User Scenarios & Testing]

## Requirement Clarity

- [ ] CHK004 Is “must not write” unambiguously limited to executable onboarding SQL rather than explanatory comments? [Clarity, Spec §FR-001]
- [ ] CHK005 Is “active backend schema” identified by repository evidence and not left as an undefined environment assumption? [Clarity, Spec §Assumptions]

## Requirement Consistency

- [ ] CHK006 Are the requirements consistent in choosing removal of the obsolete cleanup rather than reintroducing a deprecated column? [Consistency, Spec §Scope and §FR-004]
- [ ] CHK007 Do the plan and task dependencies preserve the required red-before-fix and green-after-fix sequence? [Consistency, Plan §Approach]

## Acceptance Criteria Quality

- [ ] CHK008 Can each acceptance criterion be objectively evaluated with the named focused, formatting, and full-suite commands? [Measurability, Spec §Success Criteria]
- [ ] CHK009 Does the contract requirement describe a deterministic test independent of MySQL, Docker, and ZATCA? [Measurability, Spec §Non-Functional Requirements]

## Edge Cases and Assumptions

- [ ] CHK010 Is behavior for deployments that still retain the legacy column documented without making that column a runtime requirement? [Edge Case, Spec §Assumptions]
- [ ] CHK011 Is the historical test fixture boundary called out so reviewers do not mistake it for the active backend schema? [Assumption, Research §Remaining Uncertainty]

## Notes

The checklist evaluates requirement quality only. `/speckit-implement` must not alter these
reviewer-owned markers.
