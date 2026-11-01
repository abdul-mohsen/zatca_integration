-- +goose Up
-- The zatca_master registry as README.md section 1a documents it. Existing registries are
-- adopted at version 1 and never re-run this file. The baseline has no Down section.
-- Do not run goose down on this registry.
CREATE TABLE tenant (
    id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(100)  NOT NULL,
    db_name     VARCHAR(100)  NOT NULL UNIQUE,
    zatca_env   VARCHAR(20)   NOT NULL DEFAULT 'production', -- sandbox | simulation | production
    alert_email VARCHAR(255)  NOT NULL DEFAULT '',
    enabled     TINYINT(1)    NOT NULL DEFAULT 1,
    created_at  TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_enabled (enabled)
) ENGINE=InnoDB;
