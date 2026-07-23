#!/usr/bin/env bash
# Runs the WhatsApp rate limit concurrency tests against a throwaway MongoDB
# started with docker/podman.
#
#   ./run-tests.sh

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONTAINER="ums-whatsapp-ratelimit-test-mongo"
PORT=27018

# Either runtime works; CONTAINER_RUNTIME overrides the auto-detection.
if [[ -n "${CONTAINER_RUNTIME:-}" ]]; then
  RUNTIME="$CONTAINER_RUNTIME"
elif command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  RUNTIME=docker
elif command -v podman >/dev/null 2>&1 && podman info >/dev/null 2>&1; then
  RUNTIME=podman
else
  echo "error: neither docker nor podman is available and running" >&2
  exit 1
fi
echo "==> using ${RUNTIME}"

cleanup() { "$RUNTIME" rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

echo "==> starting mongo on localhost:${PORT}"
"$RUNTIME" run -d --name "$CONTAINER" -p "${PORT}:27017" \
  -e MONGO_INITDB_ROOT_USERNAME=admin \
  -e MONGO_INITDB_ROOT_PASSWORD=testpw \
  docker.io/library/mongo:6 >/dev/null

printf '==> waiting for mongo'
for _ in $(seq 1 60); do
  if "$RUNTIME" exec "$CONTAINER" mongosh --quiet --eval 'db.runCommand({ping:1})' >/dev/null 2>&1; then
    echo " ready"; break
  fi
  printf '.'; sleep 1
done

# TestMain (pkg/grpc/service/server_test.go) reads these.
# The DB name prefix is a hardcoded const (TEST_SERVICE_), not an env var.
export USER_DB_CONNECTION_STR="localhost:${PORT}"
export USER_DB_CONNECTION_PREFIX=""
export USER_DB_USERNAME="admin"
export USER_DB_PASSWORD="testpw"

export GLOBAL_DB_CONNECTION_STR="localhost:${PORT}"
export GLOBAL_DB_CONNECTION_PREFIX=""
export GLOBAL_DB_USERNAME="admin"
export GLOBAL_DB_PASSWORD="testpw"

export DB_TIMEOUT=30
export DB_IDLE_CONN_TIMEOUT=45
export DB_MAX_POOL_SIZE=8

echo "==> running tests"
cd "$REPO"
# -mod=mod bypasses the repo's broken vendor/ (protobuf 1.36.6 in go.mod vs 1.32.0 in
# vendor/modules.txt). Pre-existing, unrelated to these tests.
go test -mod=mod -v -count=1 ./pkg/grpc/service/ -run 'TestAddPhoneNumber'