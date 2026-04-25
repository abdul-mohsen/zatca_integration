-- =============================================================================
-- ZATCA daemon — schema migration for the ifritah-go backend
-- Run inside each tenant database. MySQL 8.0+.
-- NOTE: MySQL does not support `ADD COLUMN IF NOT EXISTS` or
--       `ADD INDEX IF NOT EXISTS`. Run this script ONCE per database;
--       re-running will error on already-applied statements.
-- =============================================================================

-- 1. branch_zatca_config additions ------------------------------------------
-- Stable EGS identity. Once a branch is onboarded these MUST NOT change,
-- otherwise ZATCA sees a different device on retry.
ALTER TABLE `branch_zatca_config`
    ADD COLUMN `csr_tin`             VARCHAR(9)   NOT NULL DEFAULT ''
        COMMENT '9-digit EGS serial used in CSR CommonName',
    ADD COLUMN `csr_computer_number` VARCHAR(64)  NOT NULL DEFAULT ''
        COMMENT 'EGS UUID used in CSR SerialNumber (3-{uuid})',
    ADD COLUMN `csr_invoice_type`    VARCHAR(10)  NOT NULL DEFAULT '1100'
        COMMENT '1100=both, 1000=B2B only, 0100=B2C only';

-- Onboarding bookkeeping. State machine values:
--   not_started -> csr -> compliance -> invoices -> done
--                                        -> failed (terminal until operator clears)
ALTER TABLE `branch_zatca_config`
    ADD COLUMN `onboard_state`     VARCHAR(32)  NOT NULL DEFAULT 'not_started'
        COMMENT 'not_started|csr|compliance|invoices|done|failed',
    ADD COLUMN `last_error`        TEXT         DEFAULT NULL,
    ADD COLUMN `last_attempt_at`   DATETIME     DEFAULT NULL;

-- Hash chain (PIH) state. ZATCA requires every invoice/credit/debit produced
-- by the same EGS (branch) to reference the previous document's hash, in
-- effective_date order, regardless of doc type. Cached here for simplicity:
-- update inside the same transaction that flips the doc to state=3.
-- Reset to NULL/0 on re-onboard.
ALTER TABLE `branch_zatca_config`
    ADD COLUMN `previous_invoice_hash` VARCHAR(255) DEFAULT NULL
        COMMENT 'base64(sha256(prev_signed_xml)); null = first document',
    ADD COLUMN `last_icv`              BIGINT UNSIGNED NOT NULL DEFAULT 0
        COMMENT 'Strictly increasing per-EGS Invoice Counter Value (shared across bill/credit/debit)';

-- 2. bill: store the signed XML path so operators can find it later --------
ALTER TABLE `bill`
    ADD COLUMN `invoice_xml_path` VARCHAR(500) DEFAULT NULL
        COMMENT 'Filesystem path of signed XML (relative to xml-dir)';

-- credit_note already has invoice_uuid/hash/qr; add xml path for parity.
ALTER TABLE `credit_note`
    ADD COLUMN `invoice_xml_path` VARCHAR(500) DEFAULT NULL;

-- 3. debit_note: missing entirely from the backend. The daemon already has
--    code paths (queue.DocDebit) wired for it. Either create the table OR
--    remove the code paths. Schema below mirrors credit_note.
CREATE TABLE IF NOT EXISTS `debit_note` (
    `id`               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `bill_id`          BIGINT UNSIGNED NOT NULL,
    `state`            INT          DEFAULT NULL COMMENT '1=pending, 3=submitted',
    `note`             TEXT         DEFAULT NULL COMMENT 'KSA-10 reason',
    `created_at`       DATETIME     DEFAULT CURRENT_TIMESTAMP,
    `invoice_uuid`     CHAR(36)     DEFAULT NULL,
    `invoice_hash`     VARCHAR(128) DEFAULT NULL,
    `invoice_qr`       MEDIUMTEXT,
    `invoice_xml_path` VARCHAR(500) DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uq_dn_bill_id` (`bill_id`),
    KEY `idx_dn_state` (`state`),
    CONSTRAINT `fk_debit_note_bill` FOREIGN KEY (`bill_id`)
        REFERENCES `bill` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- 4. bill.state should track "processing" so two daemons don't double-submit.
--    The current code reads state=1 (new) and only sets state=3 (done).
--    Add an explicit transitional state value and a worker-claim index.
ALTER TABLE `bill`
    ADD INDEX `idx_bill_state_branch` (`state`, `branch_id`);

-- 5. zatca_otp should be cleared after successful onboard. No schema change
--    needed; do it from the daemon: UPDATE branch_zatca_config SET zatca_otp=''
--    WHERE branch_id = ? after step 4 succeeds. Documented here as a TODO.
