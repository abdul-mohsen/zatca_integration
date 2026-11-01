#!/usr/bin/env bash
# Tests the zatca_master goose migrations on the MySQL server at MYSQL_ADDR.
#
#   MYSQL_ADDR           host:port that goose connects to, for example 127.0.0.1:3306
#   MYSQL_ROOT_PASSWORD  password of the root account
#   MYSQL_CLIENT         command that runs the mysql client as root and reads SQL on stdin
#   GOOSE_BIN            goose CLI v3.24.3 (default: goose)
#
# Covers a fresh registry, an existing registry adopted at version 1 (its rows are backfilled),
# the same schema from both paths, a down and up round trip, and the tenant_key rules.
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
migrations="$root/migrations/zatca_master"
goose_bin="${GOOSE_BIN:-goose}"
fresh=zatca_master_fresh
legacy=zatca_master_legacy

: "${MYSQL_ADDR:?MYSQL_ADDR is required}"
: "${MYSQL_ROOT_PASSWORD:?MYSQL_ROOT_PASSWORD is required}"
: "${MYSQL_CLIENT:?MYSQL_CLIENT is required}"
read -r -a client <<< "$MYSQL_CLIENT"

run_sql() { printf '%s\n' "$1" | "${client[@]}"; }
run_db() { printf '%s\n' "$2" | "${client[@]}" "$1"; }
query() { printf '%s\n' "$2" | "${client[@]}" -N -B "$1"; }
goose_run() {
  local db="$1"
  shift
  "$goose_bin" -dir "$migrations" mysql "root:${MYSQL_ROOT_PASSWORD}@tcp(${MYSQL_ADDR})/$db" "$@"
}
version() { goose_run "$1" version 2>&1 | awk '{print $NF}'; }
# ddl prints the tenant table definition without the AUTO_INCREMENT counter.
ddl() { query "$1" 'SHOW CREATE TABLE tenant' | sed -E 's/ AUTO_INCREMENT=[0-9]+//'; }

fail() { echo "FAIL: $*" >&2; exit 1; }
check() { # check <description> <expected> <actual>
  [ "$2" = "$3" ] || fail "$1: expected '$2', got '$3'"
  echo "ok: $1"
}
expect_ok() { # expect_ok <description> <db> <sql>
  run_db "$2" "$3" >/dev/null 2>&1 || fail "$1 was rejected"
  echo "ok: $1"
}
expect_rejected() { # expect_rejected <description> <db> <sql> <error text>
  local out
  if out="$(run_db "$2" "$3" 2>&1)"; then
    fail "$1 was accepted"
  fi
  case "$out" in
    *"$4"*) echo "ok: $1" ;;
    *) fail "$1 failed with another error: $out" ;;
  esac
}
cleanup() {
  run_sql "DROP DATABASE IF EXISTS $fresh; DROP DATABASE IF EXISTS $legacy;" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Every migration after the baseline needs a Down section, so the round trip can run it.
for file in "$migrations"/*.sql; do
  name="$(basename "$file")"
  case "$name" in
    00001_*) ;;
    *) grep -q '^-- +goose Down' "$file" || fail "$name has no Down section" ;;
  esac
done
echo "ok: every migration after the baseline has a Down section"

cleanup
run_sql "CREATE DATABASE $fresh CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
goose_run "$fresh" up
check "fresh registry is at version 2" 2 "$(version "$fresh")"
check "fresh registry has both tenant_key triggers" 2 \
  "$(query "$fresh" "SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = '$fresh'")"
fresh_ddl="$(ddl "$fresh")"

goose_run "$fresh" down
check "down removes tenant_key" 0 \
  "$(query "$fresh" "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = '$fresh' AND TABLE_NAME = 'tenant' AND COLUMN_NAME = 'tenant_key'")"
check "down removes both triggers" 0 \
  "$(query "$fresh" "SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = '$fresh'")"
goose_run "$fresh" up
check "up after down restores the same schema" "$fresh_ddl" "$(ddl "$fresh")"

run_sql "CREATE DATABASE $legacy CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
run_db "$legacy" "$(cat "$migrations/00001_baseline.sql")"
goose_run "$legacy" version >/dev/null
run_db "$legacy" "INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, 1);"
run_db "$legacy" "INSERT INTO tenant (name, db_name, zatca_env, alert_email) VALUES ('Al-Faris', 'alfaris_db', 'production', ''), ('Demo', 'demo_db', 'simulation', '');"
goose_run "$legacy" up
check "adopted registry is at version 2" 2 "$(version "$legacy")"
check "existing rows get tenant_key = db_name" 2 \
  "$(query "$legacy" "SELECT COUNT(*) FROM tenant WHERE tenant_key = db_name")"
check "adopted and fresh registries have the same schema" "$fresh_ddl" "$(ddl "$legacy")"

expect_ok "a row without tenant_key is accepted" "$fresh" "INSERT INTO tenant (name, db_name) VALUES ('Demo', 'demo_db');"
check "tenant_key defaults to db_name" demo_db \
  "$(query "$fresh" "SELECT tenant_key FROM tenant WHERE db_name = 'demo_db'")"
expect_ok "an explicit tenant_key is accepted" "$fresh" "INSERT INTO tenant (name, db_name, tenant_key) VALUES ('Other', 'other_db', 'stable-key-1');"
expect_rejected "a duplicate tenant_key is rejected" "$fresh" "INSERT INTO tenant (name, db_name, tenant_key) VALUES ('Dup', 'dup_db', 'stable-key-1');" "Duplicate entry"
expect_rejected "tenant_key cannot change" "$fresh" "UPDATE tenant SET tenant_key = 'stable-key-2' WHERE db_name = 'other_db';" "tenant_key is immutable"
expect_rejected "tenant_key cannot change its case" "$fresh" "UPDATE tenant SET tenant_key = 'STABLE-KEY-1' WHERE db_name = 'other_db';" "tenant_key is immutable"
expect_rejected "tenant_key cannot gain a trailing space" "$fresh" "UPDATE tenant SET tenant_key = 'stable-key-1 ' WHERE db_name = 'other_db';" "tenant_key is immutable"
expect_ok "db_name can change, because it is the routing key" "$fresh" "UPDATE tenant SET db_name = 'other_db_moved' WHERE tenant_key = 'stable-key-1';"
expect_ok "other columns can change" "$fresh" "UPDATE tenant SET name = 'Other renamed' WHERE tenant_key = 'stable-key-1';"
expect_ok "tenant_key can be set to its current value" "$fresh" "UPDATE tenant SET tenant_key = 'stable-key-1' WHERE tenant_key = 'stable-key-1';"

echo "zatca_master migrations passed on $(query "$fresh" 'SELECT VERSION()')"
