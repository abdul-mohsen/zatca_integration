# ZATCA Go Integration — Multi-Tenant Daemon

A production-ready Go daemon for Saudi Arabia's ZATCA (Zakat, Tax and Customs Authority) e-invoicing system (Fatoora). Processes invoices, credit notes, and debit notes for multiple tenants via an embedded NATS queue with strict per-branch ordering.

## Architecture

```
┌───────────────┐         ┌──────────────────────────────────────────────────┐
│  Gin Backend  │  NATS   │              ZATCA Daemon (Docker)              │
│  (your app)   │────────►│  Embedded NATS ─► Workers ─► ZATCA APIs        │
│               │  :4222  │  JetStream       per-branch   sign + submit    │
└───────────────┘         │                  ordering     via SDK (fatoora) │
                          └──────────────────────────────────────────────────┘
                                     │                        │
                                     ▼                        ▼
                               ┌──────────┐          ┌──────────────┐
                               │  MySQL   │          │ /data/zatca- │
                               │ per-     │          │  xml/        │
                               │ tenant   │          │ signed XMLs  │
                               └──────────┘          └──────────────┘
```

**Key components:**

- **Embedded NATS** with JetStream — no external message broker needed (~15 MB RAM)
- **Per-branch ordering** — each `(tenant × branch × doc_type)` gets its own consumer
- **Combined Docker image** — Go daemon + JRE + ZATCA SDK (`fatoora`) in one container
- **Startup replay** — on boot, scans all tenant DBs for pending documents and re-publishes them
- **Email alerts** — sends failure notifications after exhausting retries

## Prerequisites

- Docker
- MySQL 8.0+
- Go 1.25+ (for local development only)

---

## 1. Database Setup

### 1a. Master Database

The daemon discovers tenants from a central `zatca_master` database. Run this **once**:

```sql
CREATE DATABASE IF NOT EXISTS zatca_master
  CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

USE zatca_master;

CREATE TABLE IF NOT EXISTS tenant (
    id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(100)  NOT NULL,
    db_name     VARCHAR(100)  NOT NULL UNIQUE,
    zatca_env   VARCHAR(20)   NOT NULL DEFAULT 'production', -- sandbox | simulation | production
    alert_email VARCHAR(255)  NOT NULL DEFAULT '',
    enabled     TINYINT(1)    NOT NULL DEFAULT 1,
    created_at  TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_enabled (enabled)
) ENGINE=InnoDB;
```

Register each tenant (one row per client company):

```sql
INSERT INTO tenant (name, db_name, zatca_env, alert_email)
VALUES ('Al-Faris Restaurant', 'alfaris_db', 'production', 'admin@alfaris.com');
```

### 1b. Per-Tenant Tables

These tables must exist **inside each tenant's database**. The full schema is in [todo/master_schema.sql](todo/master_schema.sql). The key tables:

| Table | Purpose |
|-------|---------|
| `company` | Seller info — name, name_ar, VAT number, CRN, business category |
| `store` | Physical locations under a company |
| `branch` | ZATCA EGS device (POS). Each branch registers separately with ZATCA |
| `branch_csr` | CSR configuration inputs used during onboarding (auto-filled) |
| `branch_zatca` | Credentials returned by ZATCA after onboarding (auto-filled) |
| `client` | Buyer info — if a bill has a `buyer_id` with a VAT number, it's treated as B2B |
| `bill` | Invoices. `state=1` = new, `state=3` = submitted to ZATCA |
| `bill_product` | Line items for each bill |
| `credit_note` | Credit notes against existing bills |
| `debit_note` | Debit notes against existing bills |

**Minimum data you must populate before the daemon can process:**

```sql
-- 1. Company
INSERT INTO company (name, name_ar, vat_registration_number, cr_number, business_category)
VALUES ('My Company LTD', 'شركتي المحدودة', '310175397400003', '4030000000', 'Supply activities');

-- 2. Store
INSERT INTO store (company_id, address_name) VALUES (1, 'Main Location');

-- 3. Branch
INSERT INTO branch (store_id, name, street, building, district, city, postal_code, country)
VALUES (1, 'Main Branch', 'King Fahad Road', '1234', 'Al-Olaya', 'Riyadh', '12345', 'SA');
```

---

## 2. Build & Run

### Option A: Docker (recommended)

```bash
# Build the image
docker build -t zatca-daemon .

# Run (Option 1: individual flags — safe for special characters in passwords)
docker run -d \
  --name zatca \
  -p 4222:4222 \
  -v zatca-xml:/data/zatca-xml \
  -v zatca-nats:/nats-data \
  zatca-daemon daemon \
    --db-user  "myuser" \
    --db-pass  "p@ss!w0rd#complex" \
    --db-host  "db-host" \
    --db-port  3306 \
    --master-db zatca_master \
    --nats-port  4222 \
    --nats-data  /nats-data \
    --xml-dir    /data/zatca-xml

# Run (Option 2: full DSN — only if password has no special characters)
docker run -d \
  --name zatca \
  -p 4222:4222 \
  -v zatca-xml:/data/zatca-xml \
  -v zatca-nats:/nats-data \
  zatca-daemon daemon \
    --master-dsn "user:pass@tcp(db-host:3306)/zatca_master" \
    --base-dsn   "user:pass@tcp(db-host:3306)/" \
    --nats-port  4222 \
    --nats-data  /nats-data \
    --xml-dir    /data/zatca-xml
```

### Option B: Local (development)

```bash
go build -o bin/daemon ./cmd/daemon

# Option 1: individual flags (recommended)
./bin/daemon \
  --db-user  "root" \
  --db-pass  "p@ss!w0rd#complex" \
  --db-host  "localhost" \
  --db-port  3306 \
  --nats-port  4222 \
  --xml-dir    ./xml-output

# Option 2: full DSN
./bin/daemon \
  --master-dsn "root:pass@tcp(localhost:3306)/zatca_master" \
  --base-dsn   "root:pass@tcp(localhost:3306)/" \
  --nats-port  4222 \
  --xml-dir    ./xml-output
```

### Daemon Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--db-user` | | Database username |
| `--db-pass` | | Database password (safe for special characters) |
| `--db-host` | | Database host |
| `--db-port` | `3306` | Database port |
| `--master-db` | `zatca_master` | Master database name |
| `--master-dsn` | | Full DSN for master DB (alternative to individual flags) |
| `--base-dsn` | | Full base DSN without DB name (alternative to individual flags) |
| `--nats-port` | `4222` | Port for the embedded NATS server |
| `--nats-data` | `./nats-data` | JetStream persistent storage directory |
| `--xml-dir` | `/data/zatca-xml` | Base directory for storing signed XML files |

> **Password with special characters?** Use `--db-user`/`--db-pass` flags instead of `--master-dsn`/`--base-dsn`. The individual flags use the MySQL driver's `FormatDSN()` which properly escapes all special characters.

### What happens on startup

1. Starts embedded NATS with JetStream (file-based storage, 7-day retention)
2. Connects to `zatca_master` and loads all enabled tenants
3. Opens a DB connection to each tenant's database
4. Discovers active branches per tenant
5. Launches **3 workers per branch** (bill, credit, debit) + **1 onboard worker per tenant**
6. **Replays** any pending documents found in the DB (catches items missed while the daemon was down)
7. Waits for SIGINT/SIGTERM for graceful shutdown

---

## 3. Interacting with the Daemon

Your Gin backend publishes messages to NATS. The daemon picks them up and processes them.

### Connect to NATS (once at startup)

```go
import "github.com/nats-io/nats.go"

nc, err := nats.Connect("nats://localhost:4222")
if err != nil {
    log.Fatal(err)
}
```

### Publish events after database INSERTs

```go
import (
    "encoding/json"
    "github.com/zatca-go/zatca/queue"
)

// After INSERT INTO bill → publish bill event
func publishBill(nc *nats.Conn, dbName string, billID, branchID int64) error {
    msg := queue.Message{
        DocType:  queue.DocBill,
        ID:       billID,
        BranchID: branchID,
        DBName:   dbName,
    }
    data, _ := json.Marshal(msg)
    return nc.Publish(msg.Subject(), data)
}

// After INSERT INTO credit_note → publish credit note event
func publishCreditNote(nc *nats.Conn, dbName string, creditNoteID, branchID int64) error {
    msg := queue.Message{
        DocType:  queue.DocCredit,
        ID:       creditNoteID,
        BranchID: branchID,
        DBName:   dbName,
    }
    data, _ := json.Marshal(msg)
    return nc.Publish(msg.Subject(), data)
}

// After INSERT INTO debit_note → publish debit note event
func publishDebitNote(nc *nats.Conn, dbName string, debitNoteID, branchID int64) error {
    msg := queue.Message{
        DocType:  queue.DocDebit,
        ID:       debitNoteID,
        BranchID: branchID,
        DBName:   dbName,
    }
    data, _ := json.Marshal(msg)
    return nc.Publish(msg.Subject(), data)
}

// To register a new branch with ZATCA (needs OTP from Fatoora portal)
func publishOnboard(nc *nats.Conn, dbName string, branchID int64, otp string) error {
    msg := queue.Message{
        DocType:  queue.DocOnboard,
        ID:       branchID,
        BranchID: branchID,
        DBName:   dbName,
        OTP:      otp,
    }
    data, _ := json.Marshal(msg)
    return nc.Publish(msg.Subject(), data)
}
```

A complete reference publisher is in [todo/nats_publisher.go](todo/nats_publisher.go).

### NATS Subject Format

```
zatca.{doc_type}.{db_name}.{branch_id}
```

| Subject | Example | Purpose |
|---------|---------|---------|
| `zatca.bill.alfaris_db.1` | Bill for branch 1 | Sign + report/clear invoice |
| `zatca.credit.alfaris_db.1` | Credit note for branch 1 | Sign + report/clear credit |
| `zatca.debit.alfaris_db.1` | Debit note for branch 1 | Sign + report/clear debit |
| `zatca.onboard.alfaris_db.1` | Onboard branch 1 | CSR → compliance → production |

### Message Format

```json
{
  "doc_type": "bill",
  "id": 42,
  "branch_id": 1,
  "db_name": "alfaris_db",
  "otp": ""
}
```

| Field | Type | Description |
|-------|------|-------------|
| `doc_type` | string | `"bill"`, `"credit"`, `"debit"`, or `"onboard"` |
| `id` | int64 | Primary key in the relevant table (`bill.id`, `credit_note.id`, etc.) |
| `branch_id` | int64 | Branch ID — determines which ZATCA credentials to use |
| `db_name` | string | Tenant database name (from `tenant.db_name`) |
| `otp` | string | OTP from Fatoora portal (only for `"onboard"`) |

---

## 4. Typical Workflow Example

### Step 1: Register the tenant

```sql
-- In zatca_master
INSERT INTO tenant (name, db_name, zatca_env, alert_email)
VALUES ('Demo Restaurant', 'demo_db', 'simulation', 'admin@demo.com');
```

### Step 2: Populate tenant database

```sql
-- In demo_db
INSERT INTO company (name, name_ar, vat_registration_number, cr_number)
VALUES ('Demo Restaurant LTD', 'مطعم تجريبي', '310175397400003', '4030000000');

INSERT INTO store (company_id, address_name) VALUES (1, 'Main');

INSERT INTO branch (store_id, name, street, building, district, city, postal_code)
VALUES (1, 'Branch 1', 'King Fahad Rd', '100', 'Al-Olaya', 'Riyadh', '12345');
```

### Step 3: Restart the daemon (to pick up the new tenant)

### Step 4: Onboard the branch

Get an OTP from the [Fatoora portal](https://fatoora.zatca.gov.sa), then publish:

```go
publishOnboard(nc, "demo_db", 1, "354840")
```

The daemon will:
1. Load company/branch info from DB
2. Generate CSR via the SDK
3. Request Compliance CSID from ZATCA
4. Submit 6 compliance test invoices
5. Request Production CSID
6. Save all credentials to `branch_csr` and `branch_zatca` tables

### Step 5: Create and submit invoices

```sql
-- Insert a bill
INSERT INTO bill (branch_id, effective_date, total_before_vat, total_vat, total, payment_method)
VALUES (1, NOW(), 100.00, 15.00, 115.00, '10');

-- Insert line items
INSERT INTO bill_product (bill_id, product_name, price, quantity, vat)
VALUES (1, 'Chicken Shawarma', 100.00, 1, 15.00);
```

Then publish:

```go
publishBill(nc, "demo_db", 1, 1) // billID=1, branchID=1
```

The daemon will:
1. Query the bill and its products from the DB
2. Determine B2B (standard) vs B2C (simplified) based on whether `buyer_id` has a VAT number
3. Build UBL 2.1 XML
4. Sign via SDK (`fatoora`)
5. Submit to ZATCA (ClearInvoice for B2B, ReportInvoice for B2C)
6. Update the bill row: `state=3`, `invoice_hash`, `invoice_qr`, `invoice_uuid`, `invoice_xml_path`

### Step 6: Issue a credit note

```sql
INSERT INTO credit_note (bill_id, note) VALUES (1, 'Customer complaint');
```

```go
publishCreditNote(nc, "demo_db", 1, 1) // creditNoteID=1, branchID=1
```

---

## 5. CLI Tools

Standalone CLI tools for batch processing and management (useful outside the daemon):

### Build all

```bash
go build -o bin/register_branch ./cmd/register_branch
go build -o bin/process_bills   ./cmd/process_bills
go build -o bin/credit_notes    ./cmd/credit_notes
go build -o bin/debit_notes     ./cmd/debit_notes
go build -o bin/store_status    ./cmd/store_status
```

### Register branches

```bash
# Using individual flags (safe for special characters in password)
./bin/register_branch --db-user root --db-pass "p@ss!" --db-host localhost --db-name demo_db --otp 354840 --branch 1

# Using full DSN
./bin/register_branch --dsn "user:pass@tcp(host:3306)/demo_db" --otp 354840 --branch 1

# Register all unregistered branches
./bin/register_branch --db-user root --db-pass "p@ss!" --db-host localhost --db-name demo_db --otp 354840
```

### Process pending documents

```bash
# Using individual flags
./bin/process_bills --db-user root --db-pass "p@ss!" --db-host localhost --db-name demo_db

# Using full DSN
./bin/process_bills --dsn "user:pass@tcp(host:3306)/demo_db"

# Credit notes
./bin/credit_notes --db-user root --db-pass "p@ss!" --db-host localhost --db-name demo_db

# Debit notes
./bin/debit_notes --db-user root --db-pass "p@ss!" --db-host localhost --db-name demo_db
```
```

### Check branch status

```bash
# Table output
./bin/store_status --db-user root --db-pass "p@ss!" --db-host localhost --db-name demo_db

# JSON + ping ZATCA API
./bin/store_status --db-user root --db-pass "p@ss!" --db-host localhost --db-name demo_db --json --ping
```

All CLI tools accept `--db-user`, `--db-pass`, `--db-host`, `--db-port`, `--db-name` as safe alternatives to `--dsn`. They also accept `--zatca-env` (default `production`) and `--xml-dir` (default `/data/zatca-xml`).

---

## 6. Processing Pipeline

### Bill Processing

```
bill (state=1) ──► Query bill + products from DB
                   ──► Determine B2B vs B2C
                   ──► Build UBL 2.1 XML
                   ──► SDK: sign invoice (fatoora)
                   ──► ZATCA API: ClearInvoice (B2B) or ReportInvoice (B2C)
                   ──► Update bill: state=3, hash, QR, UUID, XML path
                   ──► Save signed XML to filesystem
```

### B2B vs B2C Detection

| Condition | Type | SubType | API |
|-----------|------|---------|-----|
| `buyer_id` is NULL or buyer has no VAT | Simplified (B2C) | `0200000` | `ReportInvoice` |
| `buyer_id` points to client with VAT | Standard (B2B) | `0100000` | `ClearInvoice` |

### Payment Methods

| Code | Meaning |
|------|---------|
| `10` | Cash |
| `30` | Credit |
| `42` | Bank transfer |
| `48` | Card |

### Document Types

| Type | Code | Table |
|------|------|-------|
| Invoice | 388 | `bill` |
| Credit Note | 381 | `credit_note` |
| Debit Note | 383 | `debit_note` |

---

## 7. Retry & Error Handling

- Each message is retried up to **10 times** with exponential backoff (2s, 4s, 8s, ... capped at 5 min)
- After exhausting retries, the daemon:
  - Sends an **alert email** to the tenant's `alert_email`
  - ACKs the message (to avoid blocking the queue)
  - Leaves the document in its current state for manual handling
- On startup, the daemon **replays** any documents still pending in the DB

### SMTP Configuration (for alerts)

Set these environment variables on the daemon container:

```bash
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USER=alerts@yourcompany.com
SMTP_PASS=app-password
SMTP_FROM=alerts@yourcompany.com
```

---

## 8. Signed XML Storage

Signed XML files are saved to the filesystem at:

```
{xml-dir}/{company_id}/{branch_id}/{bill_type}/{row_id}.xml
```

Example: `/data/zatca-xml/1/3/invoice/42.xml`

In Docker, mount a volume to persist XMLs:

```bash
docker run -v zatca-xml:/data/zatca-xml ...
```

The path is also stored in the bill row as `invoice_xml_path`.

---

## 9. ZATCA Environments

| Environment | Base URL | Usage |
|-------------|----------|-------|
| `sandbox` | `https://gw-fatoora.zatca.gov.sa/e-invoicing/developer-portal` | Development/testing |
| `simulation` | `https://gw-fatoora.zatca.gov.sa/e-invoicing/simulation` | Pre-production validation |
| `production` | `https://gw-fatoora.zatca.gov.sa/e-invoicing/core` | Live |

Set per-tenant via `tenant.zatca_env` column in the master DB.

---

## 10. ZATCA Timing Rules

| Invoice Type | API | Deadline |
|---|---|---|
| **Standard (B2B)** | `ClearInvoice` | Must be cleared **before** delivering invoice to buyer |
| **Simplified (B2C)** | `ReportInvoice` | Must be reported within **24 hours** of issuance |

---

## 11. Certificate Renewal

ZATCA certificates expire after ~1 year. To renew, get a fresh OTP from the Fatoora portal and re-onboard:

```go
publishOnboard(nc, "demo_db", branchID, "new_otp")
```

Or via CLI:

```bash
./bin/register_branch --dsn "..." --branch 1 --otp <new_otp>
```

The `INSERT ... ON DUPLICATE KEY UPDATE` in the onboarding flow handles both initial registration and renewal.

---

## 12. Monitoring

| What | How | Frequency |
|---|---|---|
| ZATCA API reachable | `store_status --ping` | Every 6 hours |
| Unprocessed bills | `SELECT COUNT(*) FROM bill WHERE state < 3` | Every 5 minutes |
| Failed submissions | Daemon logs (`ERROR:` entries) | Real-time |
| Certificate expiry | Parse `production_certificate` from `branch_zatca` | Daily |
| Unregistered branches | `store_status --json` → filter `not_registered` | On new branch creation |

---

## Project Structure

```
├── cmd/
│   ├── daemon/          Daemon entry point (main, worker, email, replay)
│   ├── register_branch/ CLI: branch ZATCA registration
│   ├── process_bills/   CLI: batch invoice processing
│   ├── credit_notes/    CLI: batch credit note processing
│   ├── debit_notes/     CLI: batch debit note processing
│   └── store_status/    CLI: branch status checker
├── config/              Config struct, Environment, Taxpayer
├── client/              HTTP client for all 6 ZATCA API endpoints
├── sdk/                 ZATCA SDK wrapper (calls fatoora as subprocess)
├── invoice/             Invoice types, UBL 2.1 XML generation
├── crypto/              Hashing, XAdES signing, QR TLV encoding
├── models/              Shared API request/response types
├── zatca/               End-to-end service orchestrator (Onboard, Report, Clear)
├── processor/           Bill/credit/debit processing pipeline + onboarding
├── queue/               NATS message types and subject helpers
├── storeconfig/         Branch ZATCA config loading from DB
├── tenant/              Tenant discovery from master DB
├── todo/                Reference files (schema, publisher example)
├── Dockerfile           Combined image: Go daemon + JRE + ZATCA SDK
└── .env.example         DSN configuration reference
```
