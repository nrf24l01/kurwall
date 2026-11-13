# Deploy Kurwall on a server

This guide uses two files from GitHub: a Compose file and an `.env`. You do not
need to clone the repository, generate certificates by hand, or build the agent
distribution.

The Compose file starts PostgreSQL, ClickHouse, a small init container and the
coordinator. The init container writes the coordinator configuration and
generates the TLS CA and coordinator certificate on first start. The coordinator
image already contains the node agent binary and installer assets.

## Requirements

- Linux server (x86_64 or arm64) with Docker Engine and Compose v2.
- 2 GB RAM minimum, 4 GB recommended.
- Inbound `8080/tcp` for the dashboard/API and `9090/tcp` for agents.
- Outbound HTTPS for the container registry and ASN data (or an ASN mirror).
- A DNS name or IP address that both users and agents can reach.
- The published GHCR image is `linux/amd64`. For arm64 nodes, use an arm64
  build of the same image and set `KURWALL_COORDINATOR_IMAGE` to it.

## Ports

| Port | Purpose |
|---|---|
| `8080` | Dashboard, REST API, `/install.sh`, `/agent/*` |
| `9090` | Agent gRPC over TLS |
| `5432`, `9000` | PostgreSQL and ClickHouse, internal Compose network only |

## 1. Download the files

```bash
sudo mkdir -p /opt/kurwall
sudo chown "$USER" /opt/kurwall
cd /opt/kurwall

curl -fsSL https://raw.githubusercontent.com/nrf24l01/kurwall/main/deploy/compose.standalone.yml -o compose.yml
curl -fsSL https://raw.githubusercontent.com/nrf24l01/kurwall/main/deploy/standalone.env.example -o .env
```

You can also open those two files on GitHub and save them as `compose.yml` and
`.env`. To pin a release instead of `main`, replace `main` in the URLs with a tag.

The directory now contains only:

```
/opt/kurwall/
├── compose.yml
└── .env
```

## 2. Configure `.env`

Edit at least these values:

```dotenv
POSTGRES_PASSWORD=REPLACE_WITH_OPENSSL_RAND_HEX_24
CLICKHOUSE_PASSWORD=REPLACE_WITH_OPENSSL_RAND_HEX_24

KURWALL_BIND_ADDR=0.0.0.0

KURWALL_PUBLIC_URL=http://SERVER_IP:8080
KURWALL_GRPC_PUBLIC_ADDR=SERVER_IP:9090
KURWALL_GRPC_SERVER_NAME=kurwall-coordinator

KURWALL_TLS_SAN=DNS:kurwall-coordinator,DNS:localhost,IP:127.0.0.1,IP:SERVER_IP

KURWALL_COORDINATOR_IMAGE=ghcr.io/nrf24l01/kurwall/coordinator:latest
```

Replace `SERVER_IP` with the address users and agents use. Generate the
passwords with:

```bash
openssl rand -hex 24
```

What the settings mean:

- `KURWALL_PUBLIC_URL` is the browser/API origin. It is embedded in node install
  links and used to download `/agent/*` assets, so nodes must reach it.
- `KURWALL_GRPC_PUBLIC_ADDR` is the address agents dial. It may use a different
  hostname or port than the web URL.
- `KURWALL_GRPC_SERVER_NAME` is the TLS name agents verify. Keep
  `kurwall-coordinator` with the generated certificate.
- `KURWALL_TLS_SAN` is written into the generated coordinator certificate. Keep
  `DNS:kurwall-coordinator`; add the public DNS name or IP agents use.
- `KURWALL_BIND_ADDR` publishes both ports. When a reverse proxy terminates
  HTTPS, set `KURWALL_HTTP_BIND_ADDR=127.0.0.1` and leave
  `KURWALL_GRPC_BIND_ADDR=0.0.0.0` so agents can still reach `9090`.
- Pin `KURWALL_COORDINATOR_IMAGE` to a release tag or digest instead of
  `latest` for reproducible upgrades.
- If the default iptoasn download is slow, set
  `KURWALL_ASN_CATALOG_URL=http://www.db.mirror.stargrave.org/ip/ip2asn-combined.tsv.gz`.

The `.env` contains database passwords. Never commit it.

## 3. Start the stack

```bash
cd /opt/kurwall
docker compose up -d
docker compose ps
```

On first start the `init` container:

- writes `/config/coordinator.yaml` into the `kurwall-config` volume;
- generates the main CA and coordinator certificate into the `kurwall-tls`
  volume;
- exits. The coordinator starts after it completes.

Check it:

```bash
docker compose logs init
curl -fsS http://127.0.0.1:8080/ping
docker compose logs -f coordinator
```

If the GHCR package is private:

```bash
docker login ghcr.io
```

No certificate commands and no agent build are needed. `/install.sh` and
`/agent/*` work as soon as the coordinator is healthy.

## 4. Create the first administrator

```bash
cd /opt/kurwall
docker compose exec coordinator \
  kurwall users create-admin --username admin
```

The command prompts for a password twice. The dashboard is never open: before
this account exists, `http://SERVER_IP:8080` shows the login page with the same
command as a hint. After creating the account, sign in normally.

Accounts are managed with the `users` subcommand:

```bash
docker compose exec coordinator kurwall users list
docker compose exec coordinator \
  kurwall users reset-password --username admin
docker compose exec coordinator \
  kurwall users disable --username viewer
```

## 5. Enroll nodes

In the dashboard open **Nodes → Install node**, choose a name, assign policies
and copy the one-time command. Run it on the node as root:

```bash
curl -fsSL 'http://SERVER_IP:8080/install.sh?key=…' | sudo bash
```

The installer downloads the agent binary, requests a certificate with the deploy
key, writes `/etc/kurwall/agent.yaml`, installs the systemd unit and starts the
agent. The node appears on the Nodes page and starts sending events.

The deploy key can be used once. To move an existing agent to a different gRPC
address or TLS name, edit `/etc/kurwall/agent.yaml` and restart
`kurwall-agent`, or re-run the installer with `--force`.

## 6. HTTPS reverse proxy (optional)

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

Set these in `.env`, then run `docker compose up -d`:

```dotenv
KURWALL_HTTP_BIND_ADDR=127.0.0.1
KURWALL_GRPC_BIND_ADDR=0.0.0.0
KURWALL_PUBLIC_URL=https://kurwall.example.com
```

Do **not** put the gRPC listener behind an HTTP reverse proxy. Port `9090` is
TLS with per-node client certificates. Expose it directly, through a VPN, or
with a TCP/SNI passthrough that does not terminate TLS.

## 7. Updates

```bash
cd /opt/kurwall
docker compose pull
docker compose up -d
```

The init container updates the generated configuration and keeps the existing
certificates. Back up first, and pin `KURWALL_COORDINATOR_IMAGE` to a release tag
or digest if you want controlled upgrades.

Node agents update by re-running a fresh install link or the served installer.
Their existing config and certificate are kept unless you pass `--force`.

## 8. Backups

Back up these Compose volumes:

- `kurwall-postgres` — users, policies, nodes, deploy keys;
- `kurwall-clickhouse` — event history;
- `kurwall-enrollment` — enrollment CA for deploy-key certificates;
- `kurwall-tls` — main CA and coordinator certificate;
- `kurwall-config` — generated coordinator configuration.

Also back up `.env`. The web UI and API do not store state outside these volumes.

Losing `kurwall-tls` or `kurwall-enrollment` breaks existing agent certificates.
Losing `kurwall-config` only means the init container will regenerate it.

Find the actual volume names with:

```bash
docker volume ls | grep kurwall
```

## 9. Troubleshooting

| Symptom | Check |
|---|---|
| Coordinator restarts with `config ... no such file` | `docker compose logs init`; the `init` service must complete successfully |
| `/install.sh` or `/agent/*` returns `404` | The image is missing the baked agent distribution; pull the current coordinator image |
| Node downloads the wrong architecture or gets `404` for its binary | The image contains the agent for the image architecture; use an image built for the node architecture |
| Node cannot fetch the CA | `curl -fsS http://SERVER_IP:8080/agent/ca.crt`; the `init` service must have generated `kurwall-tls` |
| Node stays offline | Port `9090` reachable, DNS resolves, node clock correct, certificate name matches `KURWALL_GRPC_SERVER_NAME` |
| Dashboard login unavailable | Create an account with `kurwall users create-admin` |
| ASN names not loading | Outbound HTTPS to RIPEstat/iptoasn, or configure `KURWALL_ASN_CATALOG_URL` |
| Coordinator unhealthy | `docker compose logs coordinator`, then `curl -fsS http://127.0.0.1:8080/api/v1/health` |

To regenerate the CA and coordinator certificate (this invalidates all enrolled
agents):

```bash
cd /opt/kurwall
docker compose down
docker volume rm "$(docker volume ls -q | grep 'kurwall-tls')"
docker compose up -d
```

## Building from source instead

The repository also contains `deploy/compose.dev.yml`, which builds the
coordinator and web UI locally. It is intended for development; the standalone
Compose file above is the normal server path.

## Configuration reference

The [configuration reference](docs/configuration.md) documents every coordinator
and agent YAML key, environment variable, Compose variable, installer flag,
default path and volume.