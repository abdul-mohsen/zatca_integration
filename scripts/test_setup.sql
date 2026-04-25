-- ============================================================
-- Minimal DB setup for testing the ZATCA daemon queue pipeline
-- Run as MySQL root: mysql -u root -p < scripts/test_setup.sql
-- ============================================================

-- 1. Master database
CREATE DATABASE IF NOT EXISTS zatca_master;
USE zatca_master;

CREATE TABLE IF NOT EXISTS tenant (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(100) NOT NULL,
    db_name     VARCHAR(100) NOT NULL,
    zatca_env   VARCHAR(20) NOT NULL DEFAULT 'sandbox',
    alert_email VARCHAR(200) NOT NULL DEFAULT '',
    enabled     TINYINT NOT NULL DEFAULT 1
);

INSERT IGNORE INTO tenant (id, name, db_name, zatca_env, alert_email, enabled)
VALUES (1, 'Test Tenant', 'zatca_test_db', 'sandbox', '', 1);

-- 2. Tenant database
CREATE DATABASE IF NOT EXISTS zatca_test_db;
USE zatca_test_db;

CREATE TABLE IF NOT EXISTS company (
    id                              INT AUTO_INCREMENT PRIMARY KEY,
    name                            VARCHAR(200) NOT NULL DEFAULT '',
    name_ar                         VARCHAR(200) NOT NULL DEFAULT '',
    vat_registration_number         VARCHAR(15) NOT NULL DEFAULT '',
    commercial_registration_number  VARCHAR(50) NOT NULL DEFAULT '',
    business_category               VARCHAR(100) NOT NULL DEFAULT 'Supply activities'
);

INSERT IGNORE INTO company (id, name, name_ar, vat_registration_number, commercial_registration_number, business_category)
VALUES (1, 'Test Company', 'شركة اختبار', '300000000000003', '1010010000', 'Supply activities');

CREATE TABLE IF NOT EXISTS branches (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(200) NOT NULL DEFAULT '',
    company_id  INT NOT NULL,
    is_active   TINYINT NOT NULL DEFAULT 1
);

INSERT IGNORE INTO branches (id, name, company_id, is_active)
VALUES (1, 'Main Branch', 1, 1);

CREATE TABLE IF NOT EXISTS store (
    id              INT AUTO_INCREMENT PRIMARY KEY,
    branch_id       INT NOT NULL,
    company_id      INT NOT NULL DEFAULT 1,
    address_name    VARCHAR(255) NOT NULL DEFAULT '',
    street_name     VARCHAR(200) NOT NULL DEFAULT 'King Fahd Road',
    building_number VARCHAR(10) NOT NULL DEFAULT '1234',
    district        VARCHAR(100) NOT NULL DEFAULT 'Al Olaya',
    city            VARCHAR(100) NOT NULL DEFAULT 'Riyadh',
    postal_code     VARCHAR(10) NOT NULL DEFAULT '12345',
    country         VARCHAR(5) NOT NULL DEFAULT 'SA'
);

INSERT IGNORE INTO store (id, branch_id, company_id, address_name, street_name, building_number, district, city, postal_code, country)
VALUES (1, 1, 1, 'King Fahd Road, Al Olaya, Riyadh', 'King Fahd Road', '1234', 'Al Olaya', 'Riyadh', '12345', 'SA');

CREATE TABLE IF NOT EXISTS branch_zatca_config (
    branch_id                    INT NOT NULL,
    csr_org_identifier           VARCHAR(50) NOT NULL DEFAULT '',
    csr_org_unit                 VARCHAR(100) NOT NULL DEFAULT '',
    csr_org_name                 VARCHAR(200) NOT NULL DEFAULT '',
    csr_country                  VARCHAR(5) NOT NULL DEFAULT 'SA',
    csr_location                 VARCHAR(100) NOT NULL DEFAULT '',
    business_category            VARCHAR(100) NOT NULL DEFAULT '',
    seller_vat                   VARCHAR(15) NOT NULL DEFAULT '',
    seller_crn                   VARCHAR(50) NOT NULL DEFAULT '',
    street                       VARCHAR(200) NOT NULL DEFAULT '',
    building                     VARCHAR(10) NOT NULL DEFAULT '',
    district                     VARCHAR(100) NOT NULL DEFAULT '',
    postal_code                  VARCHAR(10) NOT NULL DEFAULT '',
    zatca_otp                    VARCHAR(100) NOT NULL DEFAULT '',
    zatca_csr                    TEXT,
    zatca_private_key            TEXT,
    zatca_compliance_certificate TEXT,
    zatca_compliance_secret      TEXT,
    zatca_compliance_request_id  VARCHAR(200) NOT NULL DEFAULT '',
    zatca_production_username    VARCHAR(200) NOT NULL DEFAULT '',
    zatca_production_password    TEXT,
    zatca_production_request_id  VARCHAR(200) NOT NULL DEFAULT '',
    zatca_registered_at          DATETIME,
    csr_tin                      VARCHAR(9)  NOT NULL DEFAULT '',
    csr_computer_number          VARCHAR(64) NOT NULL DEFAULT '',
    csr_invoice_type             VARCHAR(10) NOT NULL DEFAULT '1100',
    onboard_state                VARCHAR(32) NOT NULL DEFAULT 'not_started',
    last_error                   TEXT         DEFAULT NULL,
    last_attempt_at              DATETIME     DEFAULT NULL,
    previous_invoice_hash        VARCHAR(255) DEFAULT NULL,
    last_icv                     BIGINT UNSIGNED NOT NULL DEFAULT 0,
    UNIQUE KEY (branch_id)
);

-- Seed branch_zatca_config so onboarding has all required CSR fields.
-- csr_location must be 4 letters + 4 digits (BR-KSA-43); otherwise Validate() fails.
INSERT IGNORE INTO branch_zatca_config
    (branch_id, csr_org_identifier, csr_org_unit, csr_org_name, csr_country,
     csr_location, business_category, seller_vat, seller_crn,
     street, building, district, postal_code, zatca_otp,
     csr_tin, csr_computer_number, csr_invoice_type, onboard_state)
VALUES
    (1, '300000000000003', 'Test Unit', 'Test Company', 'SA',
     'RRRD2929', 'Supply activities', '300000000000003', '1010010000',
     'King Fahd Road', '1234', 'Al Olaya', '12345', '123456',
     '', '', '1100', 'not_started');

-- 3. Test user (same as daemon expects)
-- Docker MySQL init runs as root, so these always work
CREATE USER IF NOT EXISTS 'zatca_daemon'@'%' IDENTIFIED BY 'test_pass_123';
GRANT ALL PRIVILEGES ON zatca_master.* TO 'zatca_daemon'@'%';
GRANT ALL PRIVILEGES ON zatca_test_db.* TO 'zatca_daemon'@'%';
FLUSH PRIVILEGES;

-- 4. Empty branch_zatca_config — fresh setup, no branches onboarded yet
-- The onboard worker will populate this after receiving an OTP message

-- 5. Client table (for B2B invoices)
CREATE TABLE IF NOT EXISTS client (
    id              INT AUTO_INCREMENT PRIMARY KEY,
    name            VARCHAR(255) NOT NULL DEFAULT '',
    company_name    VARCHAR(255) NOT NULL DEFAULT '',
    vat_number      VARCHAR(15) NOT NULL DEFAULT '',
    address         VARCHAR(255) NOT NULL DEFAULT '',
    street          VARCHAR(255) NOT NULL DEFAULT '',
    building        VARCHAR(50) NOT NULL DEFAULT '',
    district        VARCHAR(255) NOT NULL DEFAULT '',
    city            VARCHAR(100) NOT NULL DEFAULT '',
    postal_code     VARCHAR(10) NOT NULL DEFAULT '',
    country         VARCHAR(2) NOT NULL DEFAULT 'SA',
    scheme_id       VARCHAR(10) NOT NULL DEFAULT '',
    registration_id VARCHAR(50) NOT NULL DEFAULT ''
);

-- 6. Bill table (matches actual backend columns used by bill_totals view)
CREATE TABLE IF NOT EXISTS bill (
    id                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    branch_id         INT NOT NULL,
    store_id          INT NOT NULL DEFAULT 1,
    client_id         INT DEFAULT NULL,
    effective_date    DATETIME DEFAULT NULL,
    payment_due_date  DATETIME DEFAULT NULL,
    state             INT NOT NULL DEFAULT 1,
    discount          DECIMAL(12,2) NOT NULL DEFAULT 0,
    sequence_number   VARCHAR(50) DEFAULT NULL,
    merchant_id       BIGINT DEFAULT NULL,
    note              TEXT DEFAULT NULL,
    payment_method    VARCHAR(5) NOT NULL DEFAULT '10',
    userName          VARCHAR(255) DEFAULT NULL,
    user_phone_number VARCHAR(20) DEFAULT NULL,
    maintenance_cost  DECIMAL(12,2) NOT NULL DEFAULT 0,
    invoice_hash      VARCHAR(255) DEFAULT NULL,
    qr_code           TEXT DEFAULT NULL,
    invoice_uuid      VARCHAR(100) DEFAULT NULL,
    invoice_xml_path  VARCHAR(500) DEFAULT NULL,
    deliver_date      DATETIME DEFAULT NULL,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_bill_state_branch (state, branch_id)
);

-- 7. Bill product table (line items)
CREATE TABLE IF NOT EXISTS bill_product (
    id                  BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    bill_id             BIGINT UNSIGNED NOT NULL,
    product_id          BIGINT DEFAULT NULL,
    name                VARCHAR(255) DEFAULT 'Test Product',
    price               DECIMAL(12,2) DEFAULT NULL,
    quantity            DECIMAL(12,3) DEFAULT NULL,
    vat                 DECIMAL(5,2) DEFAULT 15.00,
    discount            DECIMAL(12,2) DEFAULT 0,
    total_before_vat    DECIMAL(12,2) DEFAULT NULL,
    vat_total           DECIMAL(12,2) DEFAULT NULL,
    total_including_vat DECIMAL(12,2) DEFAULT NULL
);

-- 8. bill_totals VIEW (aggregates bill_product line totals)
CREATE OR REPLACE VIEW bill_totals AS
SELECT
    b.id, b.effective_date, b.payment_due_date, b.state, b.discount,
    b.store_id, b.sequence_number, b.merchant_id, b.maintenance_cost,
    b.note, b.userName, b.client_id, b.user_phone_number,
    b.payment_method, b.branch_id, b.deliver_date,
    ROUND(COALESCE(SUM(bp.total_before_vat),0),2) AS total_before_vat,
    ROUND(COALESCE(SUM(bp.vat_total),0),2)        AS total_vat,
    ROUND(COALESCE(SUM(bp.total_including_vat),0),2) AS total,
    b.qr_code
FROM bill b
LEFT JOIN bill_product bp ON b.id = bp.bill_id
GROUP BY b.id;

-- 9. Credit note table
CREATE TABLE IF NOT EXISTS credit_note (
    id               BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    bill_id          BIGINT UNSIGNED NOT NULL,
    state            INT DEFAULT 1,
    NOTE             TEXT,
    created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
    invoice_uuid     CHAR(36) DEFAULT NULL,
    invoice_hash     VARCHAR(128) DEFAULT NULL,
    invoice_qr       MEDIUMTEXT,
    invoice_xml_path VARCHAR(500) DEFAULT NULL
);

-- 9b. Debit note table (mirrors credit_note)
CREATE TABLE IF NOT EXISTS debit_note (
    id               BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    bill_id          BIGINT UNSIGNED NOT NULL,
    state            INT DEFAULT 1,
    note             TEXT,
    created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
    invoice_uuid     CHAR(36) DEFAULT NULL,
    invoice_hash     VARCHAR(128) DEFAULT NULL,
    invoice_qr       MEDIUMTEXT,
    invoice_xml_path VARCHAR(500) DEFAULT NULL,
    UNIQUE KEY uq_dn_bill_id (bill_id),
    KEY idx_dn_state (state)
);

-- 10. Insert mock data: a simplified bill (no client = B2C)
INSERT INTO bill (id, branch_id, store_id, client_id, effective_date, state, sequence_number, payment_method, userName)
VALUES (1, 1, 1, NULL, NOW(), 1, 'INV-0001', '10', 'Test Customer');

INSERT INTO bill_product (bill_id, product_id, name, price, quantity, vat, discount, total_before_vat, vat_total, total_including_vat)
VALUES (1, 1, 'Widget A', 100.00, 2, 15.00, 0, 200.00, 30.00, 230.00);

INSERT INTO bill_product (bill_id, product_id, name, price, quantity, vat, discount, total_before_vat, vat_total, total_including_vat)
VALUES (1, 2, 'Widget B', 50.00, 1, 15.00, 0, 50.00, 7.50, 57.50);

-- 11. Insert mock credit note against bill 1
INSERT INTO credit_note (id, bill_id, state, NOTE)
VALUES (1, 1, 1, 'Customer returned Widget A');

SELECT '=== Setup complete ===' AS status;
SELECT '  Master DB: zatca_master (1 tenant)' AS info
UNION ALL SELECT '  Tenant DB: zatca_test_db (1 company, 1 branch, 1 store)'
UNION ALL SELECT '  User: zatca_daemon / test_pass_123'
UNION ALL SELECT '  ZATCA env: sandbox';
