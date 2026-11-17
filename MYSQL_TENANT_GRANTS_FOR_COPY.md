# Temporary MySQL tenant-grant reference

Copy this setup SQL as needed, then close this draft PR without merging it. It does not change application scripts or run against a database.

Run as a MySQL administrator. Create the runtime account once; if it already exists, skip `CREATE USER`. Replace the password placeholder locally, and replace `tenant_acme` with each exact tenant database name.

```sql
CREATE USER IF NOT EXISTS 'zatca_daemon'@'%' IDENTIFIED BY 'REPLACE_WITH_A_LONG_RANDOM_PASSWORD';

GRANT SELECT ON `zatca_master`.`tenant` TO 'zatca_daemon'@'%';

GRANT SELECT ON `tenant_acme`.`branches` TO 'zatca_daemon'@'%';
GRANT SELECT ON `tenant_acme`.`company` TO 'zatca_daemon'@'%';
GRANT SELECT ON `tenant_acme`.`store` TO 'zatca_daemon'@'%';
GRANT SELECT ON `tenant_acme`.`client` TO 'zatca_daemon'@'%';
GRANT SELECT ON `tenant_acme`.`bill_product` TO 'zatca_daemon'@'%';
GRANT SELECT ON `tenant_acme`.`bill_totals` TO 'zatca_daemon'@'%';
GRANT SELECT (`id`, `state`, `branch_id`) ON `tenant_acme`.`bill` TO 'zatca_daemon'@'%';
GRANT UPDATE (`invoice_hash`, `qr_code`, `invoice_uuid`, `state`) ON `tenant_acme`.`bill` TO 'zatca_daemon'@'%';
GRANT SELECT (`id`, `bill_id`, `state`, `NOTE`) ON `tenant_acme`.`credit_note` TO 'zatca_daemon'@'%';
GRANT UPDATE (`invoice_hash`, `invoice_qr`, `invoice_uuid`, `state`) ON `tenant_acme`.`credit_note` TO 'zatca_daemon'@'%';
GRANT SELECT, INSERT, UPDATE ON `tenant_acme`.`branch_zatca_config` TO 'zatca_daemon'@'%';

SHOW GRANTS FOR 'zatca_daemon'@'%';
```

These are exact database/table/column grants; there is no `DELETE` or global privilege. Repeat the tenant grants only for approved databases whose names start with `tenant_`. For tighter network scope, replace `'%'` consistently with the daemon's source host or IP.
