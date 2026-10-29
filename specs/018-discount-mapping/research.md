# T018 Research

## Repository evidence

- `processor/types.go` already selects `bill_product.discount`, but
  `buildLines` resets the value to zero.
- `processor/query.go` selects `b.discount` but scans it into a local variable
  without storing it on `BillRow`.
- The backend schema defines:
  `bill_product.total_before_vat = ROUND(price * quantity - discount, 2)`,
  `vat_total = ROUND(total_before_vat * vat / 100, 2)`, and
  `total_including_vat = ROUND(total_before_vat + vat_total, 2)`.
- `invoice/xml.go` already emits a document allowance, but tax subtotals are
  grouped from undiscounted document-level line values and the line allowance
  is nested under `cac:Price`.
- Existing invoice tests use `float64` public fields and expect two-decimal
  output; public construction compatibility must therefore be retained while
  new arithmetic is kept decimal-safe internally.
- Prerequisite `11c0435` is the party-scheme commit and the target worktree is
  clean on `fix/zatca/h4-discount-mapping`.

## Authoritative references

- OASIS UBL 2.1 data model: https://docs.oasis-open.org/ubl/UBL-2.1.html
  (UBL basic components are `cbc:*`, aggregate components are `cac:*`).
- EN 16931/UBL syntax guidance confirms that `cac:Price/cbc:PriceAmount` is
  the item net price, while `InvoiceLine/cac:AllowanceCharge/cbc:Amount` is
  the line allowance amount (maximum two decimals):
  https://docs.peppol.eu/poacc/billing/3.0/syntax/ubl-invoice/cac-InvoiceLine/cac-Price/cbc-PriceAmount/
  and
  https://docs.peppol.eu/poacc/billing/3.0/syntax/ubl-invoice/cac-InvoiceLine/cac-AllowanceCharge/cbc-Amount/
- Frozen project ZATCA references:
  https://zatca.gov.sa/en/E-Invoicing/SystemsDevelopers/Pages/E-Invoice-specifications.aspx
  and the Electronic Invoice XML Implementation Standard listed in
  `C:\ssda\chatGPT\ifritah\specs\003-zatca-fixes\research.md`.

## Decisions

1. Add nullable bill discount plumbing to `BillRow` and all bill/credit-note
   scans that consume the shared row.
2. Copy product discounts into `LineItem`.
3. Keep the public float-shaped input API for compatibility, but calculate
   invoice amounts through an internal exact decimal/rational helper and format
   only at XML boundaries.
4. Serialize line allowances at invoice-line scope. Retain the price amount as
   the stored unit price; the line extension remains the backend-rounded net
   line amount.
5. Apply a document allowance to the configured/default allowance tax category
   when producing tax subtotals, so summary and subtotal totals reconcile.

## Remaining uncertainty

No authorized ZATCA sandbox or proprietary SDK is available in this lane.
Provider acceptance is therefore not claimed; this lane validates deterministic
XML construction only.
