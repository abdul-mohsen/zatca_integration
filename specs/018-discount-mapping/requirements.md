# T018 Requirements: Discounted Invoice XML Mapping

## Problem

The daemon reads bill-level and line-level discounts from the tenant schema but
does not carry them through invoice construction consistently. Generated UBL
can therefore omit a database discount or expose totals that disagree with the
backend's two-decimal decimal totals.

## Goals

- Preserve the bill discount and each product-row discount when building an
  invoice.
- Emit UBL allowance information for both discount scopes.
- Make line, tax, legal-total, and payable amounts use the same rounded
  two-decimal values as the backend.
- Add a focused XML-fixture regression that fails on the prerequisite base and
  passes after the fix.

## In scope

- Bill/product row mapping used by invoice, credit-note, and debit-note
  builders.
- Invoice monetary calculations and UBL allowance/tax/total serialization.
- Focused processor/invoice regression coverage and directly related docs.

## Out of scope

- Submission status, outbox, provider response, retry, PIH, or ICV redesign.
- Database schema changes or migration cleanup.
- Provider/sandbox calls and proprietary SDK artifacts.

## Functional requirements

- **FR-001:** A bill row's persisted discount is available to the invoice
  builder, including null/zero handling.
- **FR-002:** A product row's persisted discount is copied to its
  `LineItem` without changing quantity or unit price.
- **FR-003:** Bill-level discounts are represented as UBL document allowances
  with `ChargeIndicator=false`; line-level discounts are represented at their
  invoice-line scope.
- **FR-004:** Generated monetary totals are calculated from rounded decimal
  values, not binary floating-point arithmetic newly introduced by this lane.
- **FR-005:** XML line extension, tax subtotal, tax totals, tax-exclusive,
  tax-inclusive, allowance, and payable amounts agree with the fixture's
  two-decimal values.
- **FR-006:** Existing undiscounted invoice, credit-note, debit-note, and party
  mapping behavior remains unchanged.

## Acceptance criteria

1. The focused T018 fixture test fails on commit `11c0435` before the behavior
   change and its failure identifies missing discount mapping or totals.
2. The same test passes after implementation.
3. `gofmt`, targeted invoice/processor tests, and `go test ./...` pass.
4. No migration is required because the lane consumes existing `bill.discount`
   and `bill_product.discount` columns.
5. The final commit contains only T018 changes and is not pushed.

## Assumptions, constraints, risks, and unknowns

### Assumptions

- Existing backend generated product totals round line extension and VAT to
  two decimal places before summing.
- Bill-level discount is a document allowance taxed at the standard VAT rate
  unless its explicit invoice tax category/rate is provided.

### Constraints

- Preserve public invoice construction compatibility with current callers.
- Use the existing UBL namespace and serialization style.
- Do not add a provider or database dependency.

### Risks

- A backend may contain mixed VAT categories; allocation of a document
  allowance must remain deterministic and category-aware.
- The source schema exposes SQL decimal values through the daemon's existing
  nullable numeric scan types.

### Open questions resolved for this lane

- Existing columns are sufficient; no migration is needed.
- UBL document allowances belong under `Invoice/cac:AllowanceCharge`, while
  line allowances belong under `InvoiceLine/cac:AllowanceCharge`.
