# T029 Research

## Repository Evidence

1. `processor/onboard.go` already excludes `zatca_otp` from the credential `INSERT` and
   `ON DUPLICATE KEY UPDATE` clauses. The only remaining executable reference is the
   post-success `UPDATE branch_zatca_config SET zatca_otp = ''` cleanup.
2. `scripts/migration_zatca_schema.sql` adds the current onboarding columns
   (`csr_tin`, `csr_computer_number`, `csr_invoice_type`, `onboard_state`, and related fields)
   but does not add `zatca_otp`.
3. Commit `d86b600` explicitly removed the OTP from credential persistence because tenant
   schemas may not contain the column and because the message OTP is single-use. That commit
   intentionally left the cleanup as a best-effort legacy operation; T029 closes that
   remaining schema drift.
4. The requested branch is clean at `6021a43`, so the baseline contains no unrelated local
   changes to preserve.

## Resolved Unknowns

| Unknown | Evidence | Decision |
|---|---|---|
| Is the cleanup part of credential persistence? | It is a separate `db.Exec` after the credential save. | Remove only the cleanup; retain credential persistence. |
| Does the current daemon migration create `zatca_otp`? | The migration has no `ADD COLUMN zatca_otp`. | Treat the column as legacy and do not migrate it back in. |
| How can the regression run without MySQL/ZATCA? | The defect is an executable SQL string in the onboarding source. | Add a Go AST contract test that inspects SQL passed to `Exec`. |
| Should onboarding state be changed? | No evidence ties the warning to state transitions. | Keep state behavior completely out of scope. |

## Alternatives Considered

- **Re-add or migrate `zatca_otp`**: rejected because it restores a deprecated plaintext OTP
  field and conflicts with the active schema.
- **Keep the update but ignore errors**: rejected because it still emits the requested warning
  and preserves schema drift.
- **Use a live database integration test**: rejected for the focused contract because it adds
  MySQL/Docker setup and cannot be required for a deterministic unit regression.
- **Scan raw source text**: rejected in favor of parsing executable `Exec` calls so comments do
  not create false positives.

## Remaining Uncertainty

The legacy `zatca_otp` column remains in the repository's historical `scripts/test_setup.sql`
fixture. It is not used by the daemon after this change and is not the active schema source;
changing that fixture would expand the patch beyond the requested cleanup. The contract test
therefore guards runtime onboarding SQL directly.
