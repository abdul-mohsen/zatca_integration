# T027 Research

## Repository baseline

- Repository: ZATCA daemon integration.
- Starting commit: `6021a43` (`main`).
- Worktree/branch: `zatca-fix-t027-zatca` /
  `fix/zatca/m1-daemon-validation`.
- `config.Taxpayer.Validate` currently checks required values only for exact
  empty strings and checks VAT length only.
- `processor.OnboardBranch` calls taxpayer validation before constructing the
  service, but its additional location guard checks only non-empty input.
- `sdk.SDK.GenerateCSR` accepts a `config.CSRConfig` without local format
  validation.
- `zatca.Service.Onboard` and `OnboardWithCSR` can reach SDK/provider calls
  without a shared CSR preflight.

## Frozen authoritative references

### ZATCA onboarding reference

**E-invoicing Detailed Technical Guidelines, Version 2, November 2022**,
section 3.3.3.1 (CSR inputs and errors):

- The Organization Identifier is the taxpayer or group VAT registration number,
  **15 digits, starting and ending with 3**.
- Country Name is a **2-letter ISO 3166 Alpha-2** code.
- Invoice Type is a four-character functionality map using `0` and `1`.
- The table note states that **all CSR fields are mandatory** and input must
  follow the specification.
- The same section lists invalid VAT syntax, missing fields, and invalid CSR
  input as onboarding failure causes.

The reference was obtained from the official ZATCA document URL already listed
in the project research:

`https://zatca.gov.sa/en/E-Invoicing/Introduction/Guidelines/Documents/E-invoicing-Detailed-Technical-Guideline.pdf`

### Saudi National Address reference

The official Saudi Post/SPL National Address page states that a Short Address
“consists of 4 letters and 4 numbers”:

`https://splonline.com.sa/en/national-address-1/`

This resolves the location-format question without inferring a format from
length alone. The implementation accepts ASCII letters followed by ASCII
decimal digits and does not normalize or rewrite the supplied value.

## Decisions

1. Validate VAT with the complete authoritative shape: exactly 15 ASCII digits,
   first digit `3`, last digit `3`.
2. Treat whitespace-only required values as missing.
3. Validate CSR fields at the direct SDK boundary as well as the daemon
   onboarding boundary, protecting callers that bypass `Taxpayer`.
4. Validate the four-letter/four-digit short address as the CSR location.
5. Do not alter onboarding persistence/state transitions or database queries.

## Alternatives considered

- **Length-only checks:** rejected because the baseline already demonstrates the
  defect and accepts invalid VAT/location values.
- **Provider-only validation:** rejected because invalid data must not invoke
  the SDK or consume provider/OTP attempts.
- **Silent uppercasing/trimming:** rejected because it changes submitted CSR
  data; callers should supply the authoritative value explicitly.

## Reference availability

The frozen official ZATCA guideline and the official SPL short-address
reference were available. No provider credentials or proprietary SDK files were
needed.

