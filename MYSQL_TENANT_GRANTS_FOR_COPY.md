# Temporary MySQL tenant-grant reference

Copy this setup SQL as needed, then close this draft PR without merging it. It does not change application scripts or run against a database.

Run as a MySQL administrator. Create the runtime account once; if it already exists, skip `CREATE USER`. Replace the password placeholder locally. The escaped underscore in `tenant\_%` makes this match database names beginning with the literal prefix `tenant_`.

```sql
CREATE USER IF NOT EXISTS 'zatca_daemon'@'%' IDENTIFIED BY 'REPLACE_WITH_A_LONG_RANDOM_PASSWORD';

GRANT SELECT ON `zatca_master`.`tenant` TO 'zatca_daemon'@'%';
GRANT SELECT, INSERT, UPDATE ON `tenant\_%`.* TO 'zatca_daemon'@'%';

SHOW GRANTS FOR 'zatca_daemon'@'%';
```

The tenant grant covers all current and future tables in databases whose names start with `tenant_`. It grants read/insert/update only—no `DELETE`, `CREATE`, `DROP`, or global privileges. This is broader across tables than the per-table option, but avoids needing new grants when a table is added. For tighter network scope, replace `'%'` consistently with the daemon's source host or IP.
