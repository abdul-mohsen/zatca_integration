# T018 — Discounted Invoice XML Mapping

## User story

As a ZATCA daemon operator, I need invoice XML to preserve the discounts
entered in the backend so that the fiscal document and its monetary totals
match the stored bill.

## Scope

This feature maps existing bill and product discount columns into bill,
credit-note, and debit-note invoice XML. It does not change submission state,
outbox, PIH, ICV, provider calls, or database schema.

## Acceptance scenarios

1. A bill with a document discount and a product discount emits both allowance
   scopes and backend-consistent two-decimal totals.
2. A bill with null/zero discounts retains the existing undiscounted XML.
3. Credit and debit builders receive the same shared row/line discount mapping.
4. The focused fixture fails on the prerequisite base and passes after the fix.

## Success criteria

- 100% of focused discounted XML fixture assertions pass after the change.
- All existing Go tests remain green.
- XML monetary values have exactly two decimal places and reconcile from line
  extension through payable amount.
- No migration or provider dependency is introduced.
