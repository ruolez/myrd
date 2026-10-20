#!/usr/bin/env bash
# Runs server unit + integration tests against a throwaway Postgres 17.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NET=remotectl-test PG=remotectl-testpg
cleanup() { docker rm -f "$PG" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
docker network create "$NET" >/dev/null
docker run -d --name "$PG" --network "$NET" -e POSTGRES_PASSWORD=test postgres:17-alpine >/dev/null
for _ in $(seq 1 30); do docker exec "$PG" pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done
cd "$ROOT/server"
GODOCKER_OPTS="--network $NET -e TEST_DATABASE_URL=postgres://postgres:test@$PG:5432/postgres?sslmode=disable" \
  "$ROOT/scripts/godocker.sh" test "$@" ./...
