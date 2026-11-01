-- +goose Up
-- tenant_key is the stable identity of a tenant. db_name stays the routing key.
-- Existing rows get tenant_key = db_name. A new row takes db_name unless its caller sets a key.
-- Once set, a key cannot change.
ALTER TABLE tenant
  ADD COLUMN tenant_key VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL AFTER db_name;

UPDATE tenant SET tenant_key = db_name;

ALTER TABLE tenant
  MODIFY COLUMN tenant_key VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT '',
  ADD UNIQUE KEY uq_tenant_key (tenant_key);

-- +goose StatementBegin
CREATE TRIGGER tenant_key_default BEFORE INSERT ON tenant FOR EACH ROW
BEGIN
  IF NEW.tenant_key = '' THEN
    SET NEW.tenant_key = NEW.db_name;
  END IF;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tenant_key_immutable BEFORE UPDATE ON tenant FOR EACH ROW
BEGIN
  IF NEW.tenant_key <> OLD.tenant_key THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'tenant_key is immutable';
  END IF;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS tenant_key_immutable;
DROP TRIGGER IF EXISTS tenant_key_default;
ALTER TABLE tenant
  DROP INDEX uq_tenant_key,
  DROP COLUMN tenant_key;
