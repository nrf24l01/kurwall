#!/usr/bin/env bash
# Build a fully static musl agent binary (SQLite requires CGO) and copy it out
# of the builder image. Works with docker or podman.
set -euo pipefail

ENGINE=${CONTAINER_ENGINE:-docker}
IMAGE=${AGENT_IMAGE:-kurwall/agent-build:dev}
OUT=${1:-build/kurwall-agent}
VERSION=${VERSION:-dev}
COMMIT=${COMMIT:-unknown}
BUILD_DATE=${BUILD_DATE:-unknown}

mkdir -p "$(dirname "$OUT")"

"$ENGINE" build -f deploy/Dockerfile.agent \
  --build-arg VERSION="$VERSION" \
  --build-arg COMMIT="$COMMIT" \
  --build-arg BUILD_DATE="$BUILD_DATE" \
  -t "$IMAGE" .
cid=$("$ENGINE" create "$IMAGE")
trap '"$ENGINE" rm -f "$cid" >/dev/null 2>&1 || true' EXIT
"$ENGINE" cp "$cid:/usr/local/bin/kurwall-agent" "$OUT"
chmod 0755 "$OUT"

if command -v file >/dev/null 2>&1; then
  file "$OUT"
fi
if command -v ldd >/dev/null 2>&1; then
  ldd "$OUT" 2>&1 || true
fi