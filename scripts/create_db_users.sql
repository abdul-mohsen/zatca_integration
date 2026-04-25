-- =============================================================================
-- ZATCA Integration — Minimum-Privilege MySQL Users
-- =============================================================================
--
-- Two users with least-privilege access:
--
--   zatca_daemon   — The main daemon process (bill/credit processing + onboarding)
--   zatca_readonly — Optional read-only user for monitoring / status checks
--
-- Usage:
--   1. Replace 'CHANGE_ME' with strong passwords
--   2. Run this file once to create users + master DB grants
--   3. For each tenant DB, run:  CALL grant_zatca_tenant('tenant_db_name');
--
-- =============================================================================

-- ---------------------------------------------------------------------------
-- 1. CREATE USERS
-- ---------------------------------------------------------------------------
CREATE USER IF NOT EXISTS 'zatca_daemon'@'%' IDENTIFIED BY 'CHANGE_ME';
CREATE USER IF NOT EXISTS 'zatca_readonly'@'%' IDENTIFIED BY 'CHANGE_ME';

-- ---------------------------------------------------------------------------
-- 2. MASTER DATABASE (zatca_master) — tenant registry
-- ---------------------------------------------------------------------------
GRANT SELECT ON `zatca_master`.`tenant` TO 'zatca_daemon'@'%';
GRANT SELECT ON `zatca_master`.`tenant` TO 'zatca_readonly'@'%';

-- ---------------------------------------------------------------------------
-- 3. STORED PROCEDURE: call once per tenant DB to grant access
-- ---------------------------------------------------------------------------
-- Usage:  CALL grant_zatca_tenant('dev_ifritah');
--         CALL grant_zatca_tenant('client2_db');
-- ---------------------------------------------------------------------------
USE zatca_master;

DROP PROCEDURE IF EXISTS grant_zatca_tenant;

DELIMITER //
CREATE PROCEDURE grant_zatca_tenant(IN db_name VARCHAR(100))
BEGIN
    -- == zatca_daemon grants ==

    -- Read-only tables
    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`branches` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`company` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`store` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`client` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`bill_product` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`bill_totals` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    -- bill: read pending + column-level UPDATE for ZATCA results only
    SET @sql = CONCAT('GRANT SELECT (id, state, branch_id) ON `', db_name, '`.`bill` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT UPDATE (invoice_hash, qr_code, invoice_uuid, state) ON `', db_name, '`.`bill` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    -- credit_note: read pending + column-level UPDATE
    SET @sql = CONCAT('GRANT SELECT (id, bill_id, state, NOTE) ON `', db_name, '`.`credit_note` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT UPDATE (invoice_hash, invoice_qr, invoice_uuid, state) ON `', db_name, '`.`credit_note` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    -- branch_zatca_config: read credentials + write during onboarding
    SET @sql = CONCAT('GRANT SELECT, INSERT, UPDATE ON `', db_name, '`.`branch_zatca_config` TO ''zatca_daemon''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    -- == zatca_readonly grants ==
    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`branches` TO ''zatca_readonly''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`company` TO ''zatca_readonly''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`store` TO ''zatca_readonly''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`branch_zatca_config` TO ''zatca_readonly''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`bill_totals` TO ''zatca_readonly''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`bill` TO ''zatca_readonly''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    SET @sql = CONCAT('GRANT SELECT ON `', db_name, '`.`credit_note` TO ''zatca_readonly''@''%''');
    PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

    FLUSH PRIVILEGES;
END //
DELIMITER ;

-- ---------------------------------------------------------------------------
-- 4. GRANT YOUR FIRST TENANT
-- ---------------------------------------------------------------------------
CALL grant_zatca_tenant('dev_ifritah');

-- ---------------------------------------------------------------------------
-- ADDING MORE TENANTS LATER:
-- ---------------------------------------------------------------------------
-- 1. INSERT INTO zatca_master.tenant (name, db_name, zatca_env) VALUES ('New Client', 'new_client_db', 'production');
-- 2. CALL grant_zatca_tenant('new_client_db');
-- 3. Restart the daemon (it re-reads tenants on startup)
-- ---------------------------------------------------------------------------

-- VERIFY:
-- SHOW GRANTS FOR 'zatca_daemon'@'%';
-- SHOW GRANTS FOR 'zatca_readonly'@'%';
