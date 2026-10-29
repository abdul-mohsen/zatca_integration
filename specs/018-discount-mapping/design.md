# T018 Design

## Data flow

```text
bill.discount ───────────────► BillRow.Discount ─────► Invoice.Discount
bill_product.discount ───────► ProductRow.Discount ──► LineItem.Discount
price × quantity, discount, VAT ─► exact rounded invoice amounts ─► UBL XML
```

`QueryBillByID`, credit-note loading, and their shared row scans retain the
existing SQL shape and only bind the already-selected bill discount.

## Component changes

- `processor/types.go`: add nullable bill discount to `BillRow`.
- `processor/query.go`: scan the selected bill discount into the shared row.
- `processor/bill.go`: map bill and product discounts for bill invoices.
- `invoice/invoice.go`: centralize two-decimal exact amount operations while
  preserving existing public fields and method return types.
- `invoice/xml.go`: use the central calculations, reconcile tax groups after a
  document allowance, and emit line allowances at invoice-line scope.
- `processor/testdata/t018_discounted_invoice.xml` and
  `processor/bill_test.go`: compare generated XML monetary fields with the
  approved fixture.

Credit and debit builders already use the shared `BillRow` and `buildLines`;
their discount behavior therefore follows the same mapping without changing
submission flow.

## Rounding and failure behavior

- Each line net and VAT amount is rounded to two decimals before aggregation,
  matching backend generated columns.
- Document-level allowance tax is rounded independently before subtracting from
  the line tax sum.
- XML amounts are always rendered with exactly two decimal places.
- Null source discounts map to zero; negative discounts are not newly accepted
  by this lane.

## Compatibility and migration

No schema migration is required. The source columns already exist in `bill` and
`bill_product`; old rows with null/zero values retain current output.

## Test strategy

1. Add and run the fixture regression on `11c0435` after only test/data
   plumbing; capture the red assertion.
2. Implement mapping and exact calculations; rerun the same focused test.
3. Run invoice and processor package tests, then `go test ./...`.
4. Review the diff for scope isolation and document the unavailable provider
   gate.

## Traceability

| Requirement | Design element |
|---|---|
| FR-001/002 | Row fields and builder mapping |
| FR-003 | Document and line allowance writers |
| FR-004/005 | Exact amount helper and centralized XML totals |
| FR-006 | Existing builders/tests retained; no submission changes |
