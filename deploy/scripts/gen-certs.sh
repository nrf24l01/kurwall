#!/usr/bin/env bash
# Generate a private CA, coordinator server certificate and one agent client
# certificate for the Kurwall test environment.
#
# Usage:
#   NODE_ID=edge-1 COORDINATOR_SAN="DNS:kurwall-coordinator,DNS:localhost,IP:127.0.0.1,IP:192.0.2.10" \
#     ./deploy/scripts/gen-certs.sh deploy/tls
set -euo pipefail

OUT=${1:-deploy/tls}
NODE_ID=${NODE_ID:-$(hostname -s)}
SAN=${COORDINATOR_SAN:-"DNS:kurwall-coordinator,DNS:localhost,IP:127.0.0.1,IP:::1"}
DAYS=${CERT_DAYS:-825}

mkdir -p "$OUT"
# The coordinator container runs as an unprivileged user and must be able to
# traverse this directory to read its key pair. Production deployments should
# mount certificates through Docker secrets instead of a directory bind mount.
chmod 755 "$OUT"

if [ ! -f "$OUT/ca.key" ] || [ ! -f "$OUT/ca.crt" ]; then
  echo "creating CA in $OUT"
  openssl genrsa -out "$OUT/ca.key" 4096 >/dev/null 2>&1
  openssl req -x509 -new -nodes -sha256 -days 3650 \
    -key "$OUT/ca.key" \
    -subj "/CN=Kurwall Internal CA" \
    -out "$OUT/ca.crt"
else
  echo "reusing existing CA in $OUT"
fi

echo "creating coordinator certificate"
openssl genrsa -out "$OUT/coordinator.key" 2048 >/dev/null 2>&1
openssl req -new -key "$OUT/coordinator.key" -subj "/CN=kurwall-coordinator" -out "$OUT/coordinator.csr"
cat > "$OUT/coordinator.ext" <<EOF
basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=$SAN
EOF
openssl x509 -req -in "$OUT/coordinator.csr" -CA "$OUT/ca.crt" -CAkey "$OUT/ca.key" \
  -CAcreateserial -days "$DAYS" -sha256 -extfile "$OUT/coordinator.ext" -out "$OUT/coordinator.crt"

echo "creating agent certificate for CN=$NODE_ID"
openssl genrsa -out "$OUT/agent.key" 2048 >/dev/null 2>&1
openssl req -new -key "$OUT/agent.key" -subj "/CN=$NODE_ID" -out "$OUT/agent.csr"
cat > "$OUT/agent.ext" <<EOF
basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth
EOF
openssl x509 -req -in "$OUT/agent.csr" -CA "$OUT/ca.crt" -CAkey "$OUT/ca.key" \
  -CAcreateserial -days "$DAYS" -sha256 -extfile "$OUT/agent.ext" -out "$OUT/agent.crt"

rm -f "$OUT"/*.csr "$OUT"/*.ext "$OUT"/*.srl
chmod 600 "$OUT"/ca.key "$OUT"/agent.key
chmod 644 "$OUT"/*.crt
# The coordinator container runs as an unprivileged user, so the key must be
# readable by it. In production, mount keys through Docker secrets instead.
chmod 644 "$OUT/coordinator.key"

echo
echo "certificates written to $OUT"
echo "  coordinator: CN=kurwall-coordinator SAN=$SAN"
echo "  agent:       CN=$NODE_ID"
echo "Copy ca.crt, agent.crt and agent.key to /etc/kurwall/tls on the node."