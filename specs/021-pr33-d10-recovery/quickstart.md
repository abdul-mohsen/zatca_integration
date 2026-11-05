# Quickstart Validation: PR33 D10 Identity and Recovery

Run from the worktree root:

```powershell
rtk gofmt -w processor\bill.go processor\bill_test.go queue\ordered_stream_test.go
rtk go test ./processor -run 'TestVerifyBill|TestBuildBillInvoice|TestSuccessfulAnswer|TestConflict'
rtk go test ./queue -run 'TestRedelivery|TestRecordFailure|TestRecordedAnswer|TestAmbiguous'
rtk go vet ./processor ./queue
```

Expected results:

- The XML ID follows the source bill sequence, not the database row ID or ICV.
- A missing sequence is rejected before booking.
- Clean/warning/error 2xx responses classify as accepted,
  accepted-with-warning, and rejected respectively.
- A 409 is ambiguous and requires reconciliation.
- Redelivery reuses the stored signed XML and does not rebuild or resubmit a
  recorded outcome.

MySQL integration tests require `ZATCA_TEST_MYSQL_DSN` and `ZATCA_TEST_DB`.
They are intentionally not required for the local unit-test evidence.
