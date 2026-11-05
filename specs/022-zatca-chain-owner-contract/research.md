# Research: ZATCA Chain Owner and Reservation Contract

## ZATCA daemon evidence

- `cmd/daemon/main.go` creates one `queue.SQLChain` per tenant database and
  stores it in `tenantCtx.chain`; workers pass that chain to
  `queue.NewOrderedStream`.
- `queue/chain.go` reads `last_icv`, `previous_invoice_hash`, and all four
  reservation columns from `branch_zatca_config`.
- `SQLChain.Book` locks the branch row with `FOR UPDATE NOWAIT`, rejects a live
  reservation owned by another request, and writes the four reservation
  columns in one transaction.
- `SQLChain.Release` clears the four reservation columns only when both the
  reservation key and event token match.
- `SQLChain.Commit` locks and verifies the booking, inserts the signed
  `zatca_submission`, updates bill UUID/hash/QR, advances
  `last_icv`/`previous_invoice_hash`, and clears the reservation in one
  transaction.
- `queue.OrderedStream` calls `Stored` before `Prepare`/`Book`, so a committed
  invoice is redelivered from the stored signed XML. `SQLClaimStore` owns
  request claims and releases technical failures for immediate redelivery.

## Backend schema evidence

Read-only inspection of `backend-pr105-counter-cleanup` shows:

- Goose `00001_baseline.sql` owns `branch_zatca_config.last_icv` and
  `previous_invoice_hash`, plus the `zatca_submission` table.
- Goose `00003_zatca_submission_state.sql` owns durable submission state and
  `zatca_consumer_claim`.
- Goose `00005_egs_chain_reservation.sql` creates the four reservation columns
  and an all-or-none check constraint. Its comment still names the removed
  `pkg/egschain`.
- Goose `00010_zatca_invoice_record.sql` owns `zatca_submission` chain fields
  (`branch_id`, `invoice_icv`, `previous_invoice_hash`, `invoice_uuid`,
  `invoice_hash`, `invoice_kind`, `signed_xml`) and the unique
  `(branch_id, invoice_icv)` constraint.
- The backend worktree has no `pkg/egschain` directory, but its migration
  history still provides the schema required by the ZATCA daemon.

## Decisions

1. The ZATCA daemon is the single runtime chain owner. The backend owns schema
   migrations and ordinary bill writes but must not allocate ICVs or manipulate
   reservation ownership.
2. No backend migration change is required for the current daemon path. The
   backend should retain `00005` and all four columns/check constraints; only
   the stale comment should be corrected in a backend-owned follow-up.
3. If the backend ever removes these columns, it must first coordinate a
   replacement ZATCA daemon chain protocol and a migration for existing tenant
   databases. This is a breaking contract, not a cleanup-only deletion.

## External/operational uncertainty

- Local validation cannot prove every deployed tenant has all required Goose
  versions applied. Deployment must validate migration version/schema presence
  before starting the daemon.
- No live production database is inspected by this change.
