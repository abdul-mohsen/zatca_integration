#!/usr/bin/env bash
# Force a clean rebuild and restart of the zatca-daemon stack.
# Run from the repo root: ./scripts/rebuild.sh
set -euo pipefail

cd "$(dirname "$0")/.."

echo "==> git pull"
git pull --ff-only

echo "==> docker compose down"
docker compose down --remove-orphans

echo "==> docker compose build --no-cache --pull"
docker compose build --no-cache --pull

echo "==> docker compose up -d"
docker compose up -d

echo "==> tailing logs (Ctrl-C to stop)"
docker compose logs -f --tail=50 zatca-daemon
