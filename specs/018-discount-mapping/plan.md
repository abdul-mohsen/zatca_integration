# T018 Implementation Plan

1. Establish the fixture and regression assertion before behavior changes.
2. Retain the existing SQL selection while binding `bill.discount` into the
   shared row and copying both discount scopes into invoice data.
3. Replace new money arithmetic with exact internal amount operations, matching
   backend line rounding and reconciling document tax subtotals.
4. Serialize document and line allowances according to UBL aggregate scope.
5. Run focused red/green tests, gofmt, targeted invoice/processor tests, and
   `go test ./...`.
6. Review artifacts and diff, record the missing provider gate, and commit only
   T018 changes.
