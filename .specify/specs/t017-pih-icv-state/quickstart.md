# T017 Validation Quickstart

Run from the repository root:

```powershell
rtk go test ./invoice ./processor
rtk go test ./...
rtk go vet ./...
```

Expected outcomes:

- XML tests show a non-default PIH and a digit-only non-default ICV.
- Processor tests allocate invoice, credit-note, and debit-note positions from
  one branch sequence, isolate another branch, and make duplicate accepted
  commits harmless.
- Full tests and vet pass without requiring Docker, MySQL, or a live ZATCA
  endpoint.

For a real tenant, apply `scripts/migration_zatca_schema.sql` and ensure the
backend T016 integration provisions `branch_zatca_config.previous_invoice_hash`
and `last_icv`. The daemon intentionally reports a contract error if these
fields or `zatca_chain_position` are missing.
