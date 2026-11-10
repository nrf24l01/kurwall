#!/usr/bin/env bash
# Build the public agent distribution served by the coordinator:
# kernel binaries, installer assets, checksums and a small manifest.
set -euo pipefail

OUT=${1:-build/agent-dist}
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
BUILD_DATE=${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
ARCH=${AGENT_ARCH:-$(uname -m)}

case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *)
    echo "unsupported build architecture: $ARCH" >&2
    exit 1
    ;;
esac

mkdir -p "$OUT"
BIN="$OUT/kurwall-agent-linux-$ARCH"

if [ -n "${AGENT_BINARY:-}" ]; then
  install -m 0755 "$AGENT_BINARY" "$BIN"
elif [ "${USE_EXISTING:-0}" = "1" ] && [ -f build/kurwall-agent ]; then
  install -m 0755 build/kurwall-agent "$BIN"
else
  VERSION="$VERSION" COMMIT="$COMMIT" BUILD_DATE="$BUILD_DATE" \
    ./deploy/scripts/build-agent-static.sh "$BIN"
fi

install -m 0644 deploy/systemd/kurwall-agent.service "$OUT/kurwall-agent.service"
install -m 0644 deploy/sysctl/99-kurwall.conf "$OUT/99-kurwall.conf"
if [ -f deploy/tls/ca.crt ]; then
  install -m 0644 deploy/tls/ca.crt "$OUT/ca.crt"
else
  echo "warning: deploy/tls/ca.crt is missing; the installer will require an existing CA" >&2
fi

printf '%s\n' "$VERSION" > "$OUT/version.txt"
(cd "$OUT" && sha256sum kurwall-agent-linux-* > checksums.txt)

{
  printf '{\n'
  printf '  "version": "%s",\n' "$VERSION"
  printf '  "commit": "%s",\n' "$COMMIT"
  printf '  "builtAt": "%s",\n' "$BUILD_DATE"
  printf '  "binaries": {'
  first=1
  for file in "$OUT"/kurwall-agent-linux-*; do
    [ -f "$file" ] || continue
    name=$(basename "$file")
    target=${name#kurwall-agent-linux-}
    size=$(wc -c < "$file" | tr -d ' ')
    hash=$(sha256sum "$file" | awk '{print $1}')
    if [ "$first" -eq 1 ]; then
      printf '\n'
      first=0
    else
      printf ',\n'
    fi
    printf '    "linux-%s": {"file": "%s", "sha256": "%s", "size": %s}' "$target" "$name" "$hash" "$size"
  done
  printf '\n  }\n}\n'
} > "$OUT/manifest.json"

echo "agent distribution written to $OUT"
ls -lh "$OUT"