# ZATCA Go Integration Library — Implementation Plan

## Overview
A Go library for integrating with ZATCA's e-invoicing system (Fatoora).  
Covers: CSR generation (via SDK Docker), all 6 APIs, UBL 2.1 XML generation, 
invoice signing, QR codes, and hash chaining.

---

## Environments (switched via `.env`)
| Name       | Base URL                                                        |
|------------|-----------------------------------------------------------------|
| Sandbox    | `https://gw-fatoora.zatca.gov.sa/e-invoicing/developer-portal` |
| Simulation | `https://gw-fatoora.zatca.gov.sa/e-invoicing/simulation`       |
| Production | `https://gw-fatoora.zatca.gov.sa/e-invoicing/core`             |

---

## Phase 1 — Project Skeleton & Config  
**Goal:** Go module, folder structure, .env loading, config types.

```
zatca_go_integration/
├── .env.example            # template
├── go.mod / go.sum
├── config/
│   └── config.go           # load .env, expose Config struct
├── client/
│   └── (Phase 2)
├── sdk/
│   └── (Phase 3)
├── invoice/
│   └── (Phase 4)
├── crypto/
│   └── (Phase 5)
├── models/
│   └── models.go           # shared request/response types
├── cmd/
│   └── example/main.go     # runnable demo
└── docker/
    └── (Phase 3)
```

Deliverable: `go run ./cmd/example` prints loaded config.  
Test: unit test for config loading.

---

## Phase 2 — API Client (HTTP layer)
**Goal:** HTTP client that can call all 6 ZATCA endpoints with proper auth.

### Auth
- Compliance APIs: Basic auth (base64-encoded `username:password`)
  - username = binary security token (from CSID response)
  - password = secret (from CSID response)
- All requests need: `Accept-Version: V2`, `Accept-Language: en`

### Endpoints to implement
| # | Name                      | Method | Path                          | Auth             |
|---|---------------------------|--------|-------------------------------|------------------|
| 1 | Compliance CSID           | POST   | /compliance                   | OTP header       |
| 2 | Production CSID (onboard) | POST   | /production/csids             | Compliance creds |
| 3 | Production CSID (renewal) | PATCH  | /production/csids             | Production creds |
| 4 | Compliance Invoice Check  | POST   | /compliance/invoices          | Compliance creds |
| 5 | Reporting                 | POST   | /invoices/reporting/single    | Production creds |
| 6 | Clearance                 | POST   | /invoices/clearance/single    | Production creds |

Deliverable: `client.ComplianceCSID(csr, otp)` etc. with proper error types.  
Test: unit tests with mock HTTP server.

---

## Phase 3 — Docker SDK Wrapper
**Goal:** Run ZATCA SDK commands inside Docker container.

### SDK Commands to wrap
- `generateCSR` — generate Certificate Signing Request  
- `validateInvoice` — offline validation (optional but useful)

### Approach
- Dockerfile: JDK 11 + ZATCA SDK JAR mount
- Go wrapper: `sdk.GenerateCSR(props)` runs `docker exec ...`
- CSR config properties: CN, serial, org, country, invoice type, location

Deliverable: `sdk.GenerateCSR(...)` returns CSR string.  
Test: integration test (requires Docker + JAR).

---

## Phase 4 — UBL 2.1 XML Invoice Generation
**Goal:** Build compliant XML invoices from Go structs.

### Invoice types
- **Standard Invoice (B2B)** — type code `388`, subtype `0100000`
- **Simplified Invoice (B2C)** — type code `388`, subtype `0200000`
- **Standard Credit Note** — type code `381`, subtype `0100000`
- **Simplified Credit Note** — type code `381`, subtype `0200000`
- **Standard Debit Note** — type code `383`, subtype `0100000`
- **Simplified Debit Note** — type code `383`, subtype `0200000`

### Key XML sections
1. UBL extensions (signature, QR placeholder)
2. Invoice metadata (UUID, ICV, issue date/time)
3. Supplier party (seller info, TIN, CRN, address)
4. Customer party (buyer info — required for B2B)
5. Tax totals (VAT breakdown by category)
6. Line items (description, qty, price, tax)
7. Legal monetary total
8. Payment means

### Compliance rules
- UBL 2.1 XSD
- EN 16931 subset
- KSA-specific business rules

Deliverable: `invoice.NewStandardInvoice(data).ToXML()` → valid XML string.  
Test: generate XML, validate structure matches expected output.

---

## Phase 5 — Cryptographic Operations
**Goal:** Sign invoices, generate QR codes, maintain hash chain.

### Operations
1. **Invoice Hash** — SHA-256 of canonicalized XML (C14N)
2. **Digital Signature** — ECDSA with secp256k1 key on invoice hash
3. **QR Code (Simplified)** — TLV-encoded, base64:
   - Tag 1: Seller name
   - Tag 2: VAT number  
   - Tag 3: Timestamp (ISO 8601)
   - Tag 4: Total with VAT
   - Tag 5: VAT amount
   - Tag 6: Invoice hash
   - Tag 7: ECDSA signature
   - Tag 8: Public key
   - Tag 9: Certificate signature
4. **Hash Chain** — each invoice stores previous invoice hash (PIH)

Deliverable: `crypto.SignInvoice(xml, privateKey)` → signed XML with QR.  
Test: sign sample invoice, verify signature, decode QR.

---

## Phase 6 — End-to-End Flows & CLI
**Goal:** Wire everything together into usable workflows.

### Flow A: Onboarding
1. Generate keypair (secp256k1)
2. Generate CSR via SDK Docker
3. Call Compliance CSID API → get compliance cert + secret
4. Submit 6 compliance invoices (standard + simplified × invoice/credit/debit)
5. Call Production CSID API → get production cert + secret
6. Store credentials

### Flow B: Report Simplified Invoice (B2C)
1. Build XML invoice
2. Sign it + add QR
3. Base64 encode
4. POST to Reporting API
5. Parse response (warnings/errors)

### Flow C: Clear Standard Invoice (B2B)
1. Build XML invoice
2. Sign it (no QR needed pre-clearance)
3. Base64 encode
4. POST to Clearance API
5. Receive back cleared XML with ZATCA stamp + QR
6. Store cleared version

Deliverable: CLI commands: `zatca onboard`, `zatca report`, `zatca clear`.  
Test: full flow against sandbox.

---

## Phase 7 — Testing & Hardening
- Unit tests for each package
- Integration tests against sandbox
- Error handling review
- README with usage examples

---

## Execution Order
```
Phase 1 → Phase 2 → Phase 3 → Phase 4 → Phase 5 → Phase 6 → Phase 7
         (parallel: 3+4 can overlap)
```

Each phase: implement → test → commit → move on.

## Questions Before Starting
None blocking — Phase 1 can start now.  
SDK JAR needed by Phase 3 (user will provide).
