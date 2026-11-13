# Deploy Kurwall on a server

This is the complete guide for a single-host deployment with Docker Compose:
PostgreSQL, ClickHouse, the coordinator and the dashboard run in containers.
Node agents are installed separately on the Linux hosts they protect.

For local development, see the quick start in [README.md](README.md). For every
option, see the [configuration reference](docs/configuration.md).

## Requirements

- Linux server (x86_64 or arm64) with Docker Engine and Compose v2.
- 2 GB RAM minimum, 4 GB recommended.
- Inbound `8080/tcp` for the dashboard/API and `9090/tcp` for agents.
- Outbound HTTPS for the container registry and ASN data (or an ASN mirror).
- A DNS name or IP address that both users and agents can reach.

## Ports

| Port | Purpose |
|---|---|
| `8080` | Dashboard, REST API, `/install.sh`, `/agent/*` |
| `9090` | Agent gRPC over TLS |
| `5432`, `9000` | PostgreSQL and ClickHouse, internal Compose network only |

## 1. Get the code

```bash
sudo mkdir -p /opt/kurwall
sudo chown "$USER" /opt/kurwall
git clone https://github.com/nrf24l01/kurwall.git /opt/kurwall
cd /opt/kurwall
```

If you copy the tree instead, exclude local state:

```bash
rsync -av --exclude .git --exclude deploy/.env --exclude deploy/tls \
  ./ user@server:/opt/kurwall/
```

`deploy/.env` and `deploy/tls` contain secrets and are git-ignored. Never commit
them.

## 2. Certificates

The coordinator and agents use mutual TLS. The main CA signs the coordinator
certificate and manually issued agent certificates. A separate enrollment CA is
generated on first start and signs agents enrolled through one-time deploy keys.

Run from the repository root:

```bash
NODE_ID=seed \
COORDINATOR_SAN="DNS:kurwall-coordinator,DNS:localhost,IP:127.0.0.1,IP:SERVER_IP" \
  ./deploy/scripts/gen-certs.sh deploy/tls
```

Replace `SERVER_IP` with the server address agents use. If agents connect to a
DNS name that they should verify directly, add it to `COORDINATOR_SAN` and set
`KURWALL_GRPC_SERVER_NAME` to that name. The default `kurwall-coordinator` name
works with the generated certificate.

This creates:

| File | Purpose |
|---|---|
| `deploy/tls/ca.crt` | Main CA, also served to agents |
| `deploy/tls/ca.key` | Main CA private key, keep offline |
| `deploy/tls/coordinator.crt` / `.key` | Coordinator server certificate |
| `deploy/tls/agent.crt` / `.key` | Seed agent certificate, testing only |

Back up `deploy/tls`. If you regenerate the CA, run `make agent-dist` again so
the served `ca.crt` matches.

## 3. Configure `.env`

```bash
cd /opt/kurwall/deploy
cp .env.example .env
```

Edit at least these values:

```dotenv
POSTGRES_PASSWORD=REPLACE_WITH_OPENSSL_RAND_HEX_24
CLICKHOUSE_PASSWORD=REPLACE_WITH_OPENSSL_RAND_HEX_24

KURWALL_HTTP_BIND_ADDR=0.0.0.0
KURWALL_GRPC_BIND_ADDR=0.0.0.0

KURWALL_PUBLIC_URL=http://SERVER_IP:8080
KURWALL_GRPC_PUBLIC_ADDR=SERVER_IP:9090
KURWALL_GRPC_SERVER_NAME=kurwall-coordinator

KURWALL_AGENT_DIST_DIR=../build/agent-dist
KURWALL_COORDINATOR_IMAGE=ghcr.io/nrf24l01/kurwall/coordinator:latest
```

Generate the passwords with:

```bash
openssl rand -hex 24
```

What the settings mean:

- `KURWALL_PUBLIC_URL` is the browser/API origin. It is also embedded in node
  install links and used to download `/agent/*` assets, so nodes must reach it.
- `KURWALL_GRPC_PUBLIC_ADDR` is the address agents dial. It may use a different
  hostname or port than the web URL.
- `KURWALL_GRPC_SERVER_NAME` is the TLS name agents verify. Keep
  `kurwall-coordinator` when using the coordinator certificate. Set the external
  name only when a TCP proxy presents a certificate for that name.
- `KURWALL_AGENT_DIST_DIR` is relative to `deploy/`. Use
  `../build/agent-dist` when you build at the repository root. If you upload the
  distribution into `deploy/agent-dist`, use `./agent-dist`.
- Pin `KURWALL_COORDINATOR_IMAGE` to a release tag or digest instead of
  `latest` for reproducible upgrades.
- If the default iptoasn download is slow from your network, set
  `KURWALL_ASN_CATALOG_URL=http://www.db.mirror.stargrave.org/ip/ip2asn-combined.tsv.gz`.

If a reverse proxy terminates HTTPS, bind the dashboard to loopback
(`KURWALL_HTTP_BIND_ADDR=127.0.0.1`) and set `KURWALL_PUBLIC_URL` to the HTTPS
origin.

## 4. Build the agent distribution

The coordinator serves the installer, agent binary and CA from this directory.
Build it **after** generating the certificates:

```bash
cd /opt/kurwall
make agent-dist
```

The command uses Docker to build a static musl agent and writes
`build/agent-dist/` with the binary, systemd unit, sysctl file, `ca.crt`,
`version.txt`, `checksums.txt` and `manifest.json`.

If this directory is missing or empty, node install links return `404`.

## 5. Start the stack

```bash
cd /opt/kurwall/deploy
docker compose -f compose.prod.yml pull
docker compose -f compose.prod.yml up -d
docker compose -f compose.prod.yml ps
curl -fsS http://127.0.0.1:8080/ping
```

If the GHCR package is private:

```bash
docker login ghcr.io
```

Follow the logs with:

```bash
docker compose -f compose.prod.yml logs -f coordinator
```

## 6. Create the first administrator

```bash
cd /opt/kurwall/deploy
docker compose -f compose.prod.yml exec coordinator \
  kurwall users create-admin --username admin
```

The command prompts for a password twice. Then open
`http://SERVER_IP:8080` and sign in.

Accounts are managed with the `users` subcommand:

```bash
docker compose -f compose.prod.yml exec coordinator kurwall users list
docker compose -f compose.prod.yml exec coordinator \
  kurwall users reset-password --username admin
docker compose -f compose.prod.yml exec coordinator \
  kurwall users disable --username viewer
```

## 7. Enroll nodes

In the dashboard open **Nodes → Install node**, choose a name, assign policies
and copy the one-time command. Run it on the node as root:

```bash
curl -fsSL 'http://SERVER_IP:8080/install.sh?key=…' | sudo bash
```

The installer downloads the binary, requests a certificate with the deploy key,
writes `/etc/kurwall/agent.yaml`, installs the systemd unit and starts the
agent. The node appears on the Nodes page and starts sending events.

The deploy key can be used once. To move an existing agent to a different gRPC
address or TLS name, edit `/etc/kurwall/agent.yaml` and restart
`kurwall-agent`, or re-run the installer with `--force`.

### Offline installation

Copy the binary, `agent.yaml`, the systemd unit and certificates to the node,
then run:

```bash
./deploy/scripts/install-agent.sh <binary> <agent.yaml> <unit>
```

Place `ca.crt`, `agent.crt` and `agent.key` in `/etc/kurwall/tls` first. This
path is for manually issued certificates; deploy-key enrollment still needs the
coordinator.

## 8. HTTPS reverse proxy (optional)

Keep the dashboard on loopback and terminate TLS in front of it. Example with
Caddy:

```caddyfile
kurwall.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Nginx equivalent, including the WebSocket upgrade:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

Then set `KURWALL_HTTP_BIND_ADDR=127.0.0.1` and
`KURWALL_PUBLIC_URL=https://kurwall.example.com` in `.env`, and restart the
coordinator.

Do **not** put the gRPC listener behind an HTTP reverse proxy. Port `9090` is
TLS with per-node client certificates. Expose it directly, through a VPN, or
with a TCP/SNI passthrough that does not terminate TLS.

## 9. Updates

```bash
cd /opt/kurwall
git pull
make agent-dist

cd deploy
docker compose -f compose.prod.yml pull
docker compose -f compose.prod.yml up -d
```

To build a release on the server instead of pulling the image:

```bash
cd /opt/kurwall
docker compose -f deploy/compose.dev.yml up -d --build
```

Node agents update by re-running a fresh install link or the served installer.
Their existing config and certificate are kept unless you pass `--force`.

## 10. Backups

Back up:

- the `kurwall-postgres` volume (users, policies, nodes, deploy keys);
- the `kurwall-clickhouse` volume (event history);
- the `kurwall-enrollment` volume (enrollment CA);
- `deploy/.env` and `deploy/tls`.

Losing the `kurwall-enrollment` volume invalidates certificates for every
auto-enrolled node. Manually issued agent certificates signed by the main CA
continue to work.

## 11. Troubleshooting

| Symptom | Check |
|---|---|
| `/install.sh` or `/agent/*` returns `404` | `KURWALL_AGENT_DIST_DIR` points at an empty directory; run `make agent-dist` |
| Node cannot fetch the CA | `make agent-dist` ran before `deploy/tls/ca.crt` existed; rerun it |
| Node stays offline | Port `9090` reachable, DNS resolves, node clock correct, certificate name matches `KURWALL_GRPC_SERVER_NAME` |
| Dashboard login unavailable | Create an account with `kurwall users create-admin` |
| ASN names not loading | Outbound HTTPS to RIPEstat/iptoasn, or configure `KURWALL_ASN_CATALOG_URL` |
| Coordinator unhealthy | `docker compose -f compose.prod.yml logs coordinator`, then `curl -fsS http://127.0.0.1:8080/api/v1/health` |

## Configuration reference

The [configuration reference](docs/configuration.md) documents every coordinator
and agent YAML key, environment variable, Compose variable, installer flag,
default path and volume.