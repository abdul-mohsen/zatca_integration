#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="$repo_dir/docker-compose.yml"

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

grep -Fq 'NATS_USER: ${NATS_USER:?NATS_USER must be set in .env}' "$compose_file" ||
    fail "production Compose must require NATS_USER"
grep -Fq 'NATS_PASSWORD: ${NATS_PASSWORD:?NATS_PASSWORD must be set in .env}' "$compose_file" ||
    fail "production Compose must require NATS_PASSWORD"
grep -Fq 'NATS_ALLOW_UNAUTHENTICATED: "false"' "$compose_file" ||
    fail "production Compose must disable unauthenticated NATS explicitly"
grep -Fq '"${NATS_PORT:-4222}:4222"' "$compose_file" ||
    fail "production Compose must retain the deployment-compatible NATS port"
grep -Fq '"${NATS_MONITOR_PORT:-8222}:8222"' "$compose_file" ||
    fail "production Compose must retain the authenticated monitor port"

if grep -Fq 'NATS_ALLOW_UNAUTHENTICATED: "true"' "$compose_file"; then
    fail "production Compose must not enable unauthenticated NATS"
fi

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    (
        cd "$repo_dir"
        ZATCA_SDK_PATH="$repo_dir" \
        DB_PASS=contract-only \
        ENCRYPT_KEY=contract-only \
        MONITOR_PASS=contract-only \
        NATS_USER=contract-user \
        NATS_PASSWORD=contract-password \
        docker compose config --quiet
    )
fi

echo "PASS: production NATS Compose contract"
