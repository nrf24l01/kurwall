# Kurwall configuration reference

This document lists the coordinator, agent, Compose and installer options.
The coordinator reads YAML from `APP_CONFIG_PATH`; environment variables
override the values explicitly listed below. Unknown YAML keys are rejected.

Precedence:

1. Built-in defaults.
2. YAML file (`deploy/coordinator.yaml` or `/etc/kurwall/agent.yaml`).
3. Environment variables (only the variables listed here).

Durations use Go duration syntax: `500ms`, `30s`, `5m`, `12h`, `168h`.

---

## Coordinator (`deploy/coordinator.yaml`)

### `server`

| Option | Default | Environment | Notes |
|---|---|---|---|
| `http_addr` | `:8080` | `KURWALL_HTTP_ADDR` | REST/dashboard listen address |
| `grpc_addr` | `:9090` | `KURWALL_GRPC_ADDR` | Agent gRPC listen address |
| `web_static_dir` | `./web/dist` | `KURWALL_WEB_STATIC_DIR` | Container uses `/app/web` |
| `public_url` | empty (example: `http://localhost:8080`) | `KURWALL_PUBLIC_URL` | Public HTTP origin used in install links and cookies |
| `grpc_public_addr` | empty | `KURWALL_GRPC_PUBLIC_ADDR` | Address agents dial; derived from `public_url` and the gRPC port when empty |
| `grpc_server_name` | `kurwall-coordinator` | `KURWALL_GRPC_SERVER_NAME` | TLS name agents verify for gRPC |
| `agent_dist_dir` | `./agent-dist` | `KURWALL_AGENT_DIST_DIR` | Directory served at `/agent`; container uses `/app/agent-dist` |
| `enrollment_dir` | `/var/lib/kurwall/enrollment` | `KURWALL_ENROLLMENT_DIR` | Persistent enrollment CA directory |
| `read_timeout` | `30s` | — | HTTP read timeout |
| `idle_timeout` | `120s` | — | HTTP idle timeout |

`public_url` should be the HTTPS origin users and nodes can reach, for example
`https://kurwall.example.com`. Setting it to HTTPS also marks session cookies
`Secure` (when the reverse proxy forwards `X-Forwarded-Proto: https`).

### Separate web and gRPC endpoints

The dashboard/API and the agent gRPC listener can use different external hosts.
Only the coordinator's internal listen addresses are coupled; the advertised
addresses are not.

```yaml
server:
  public_url: "https://panel.example.com"       # dashboard, install links
  grpc_public_addr: "grpc.example.com:9090"     # address agents dial
  grpc_server_name: "kurwall-coordinator"       # TLS name agents verify
```

- `public_url` is used for browser cookies, install links and agent asset URLs.
- `grpc_public_addr` is embedded in the installer as `coordinator_addr`.
- `grpc_server_name` is embedded as `tls.server_name`. Keep
  `kurwall-coordinator` when the coordinator's own certificate is used. Set it
  to the external name when a TCP proxy/load balancer serves gRPC with a
  certificate issued for that name.
- If the coordinator terminates TLS itself, its certificate must cover
  `grpc_server_name`. Add it to `COORDINATOR_SAN` when running
  `deploy/scripts/gen-certs.sh`.
- gRPC must reach the coordinator as TLS, not through an HTTP reverse proxy.
  Use a TCP/SNI passthrough or expose the gRPC port directly.

The dashboard's **Settings → Dependencies** panel shows the resolved public URL
and gRPC address, which makes a mismatch easy to spot after deployment.

Changing these values affects **new installs**. Existing agents keep the
`coordinator_addr` and `tls.server_name` already written to
`/etc/kurwall/agent.yaml`. To move them, edit that file and restart
`kurwall-agent`, or re-run the installer with `--force` (which rewrites the
generated configuration).

### `postgres`

| Option | Default | Environment |
|---|---|---|
| `host` | `127.0.0.1` | `POSTGRES_HOST` |
| `port` | `5432` | `POSTGRES_PORT` |
| `user` | `kurwall` | `POSTGRES_USER` |
| `password` | empty | `POSTGRES_PASSWORD` |
| `database` | `kurwall` | `POSTGRES_DB` |
| `sslmode` | `disable` | `POSTGRES_SSLMODE` |
| `max_conns` | `10` | — |
| `connect_timeout` | `5s` | — |
| `statement_timeout` | `30s` | — |

### `clickhouse`

| Option | Default | Environment |
|---|---|---|
| `addr` | `127.0.0.1:9000` | `CLICKHOUSE_ADDR` |
| `database` | `kurwall` | `CLICKHOUSE_DB` |
| `username` | `default` | `CLICKHOUSE_USER` |
| `password` | empty | `CLICKHOUSE_PASSWORD` |
| `dial_timeout` | `5s` | — |
| `max_open_conns` | `10` | — |
| `max_idle_conns` | `5` | — |
| `insert_batch_size` | `2000` | — |
| `insert_interval` | `500ms` | — |
| `query_timeout` | `15s` | — |

Event tables keep 30 days by ClickHouse TTL. The dashboard's date-time filter
can query any window up to 90 days, but data older than the TTL is gone.

### `tls`

| Option | Default | Environment | Notes |
|---|---|---|---|
| `enabled` | `true` | — | Coordinator and agent mTLS |
| `ca_file` | `/etc/kurwall/tls/ca.crt` | `KURWALL_TLS_CA_FILE` | Main agent CA |
| `cert_file` | `/etc/kurwall/tls/coordinator.crt` | `KURWALL_TLS_CERT_FILE` | Coordinator server certificate |
| `key_file` | `/etc/kurwall/tls/coordinator.key` | `KURWALL_TLS_KEY_FILE` | Coordinator private key |
| `require_client_cert` | `true` | — | Reject agents without a client certificate |

The main CA signs manually issued agent certificates. A separate enrollment CA
is generated under `enrollment_dir` and signs certificates requested with
one-time deploy keys. The gRPC listener trusts both CAs.

### `asn`

| Option | Default | Environment |
|---|---|---|
| `enabled` | `true` | — |
| `prefix_url_template` | RIPEstat announced-prefixes URL | `KURWALL_ASN_PREFIX_URL` |
| `holder_url_template` | RIPEstat AS-overview URL | `KURWALL_ASN_HOLDER_URL` |
| `refresh_interval` | `12h` | — |
| `retry_interval` | `5m` | — |
| `request_timeout` | `10s` | — |
| `max_prefixes` | `20000` | — |
| `catalog_enabled` | `true` | — |
| `catalog_url` | `https://iptoasn.com/data/ip2asn-combined.tsv.gz` | `KURWALL_ASN_CATALOG_URL` |
| `catalog_refresh_interval` | `168h` | — |
| `catalog_retry_interval` | `5m` | — |
| `catalog_timeout` | `90s` | — |

`asn.enabled` controls ASN rule resolution. The catalog (ASN directory, IP→ASN
ranges and Traffic ASN/country labels) loads when `catalog_enabled` is true,
even if rule resolution is disabled.

If iptoasn is unreachable or too slow from your network, point
`KURWALL_ASN_CATALOG_URL` at a mirror. A current mirror used during testing:

```
http://www.db.mirror.stargrave.org/ip/ip2asn-combined.tsv.gz
```

The mirror is third-party display data and is not authenticated; prefer HTTPS
where available.

### `agent` (published agent settings)

These values are sent to agents in policy snapshots.

| Option | Default |
|---|---|
| `tls_inspector_ports` | `[443]` |
| `dns_inspection` | `true` |
| `tls_inspection` | `true` |
| `batch_max_events` | `500` |
| `flush_interval` | `1s` |
| `offline_after` | `90s` |

### `auth`

| Option | Default | Environment |
|---|---|---|
| `api_token` | empty | `KURWALL_API_TOKEN` |

User accounts are stored in PostgreSQL and created with the `users` CLI. The
dashboard always requires a session; there is no open mode. If no accounts exist,
the login page shows the command for creating the first administrator. A
configured API token remains an administrator bearer credential for automation.

### `ingest`

| Option | Default |
|---|---|
| `max_batch_events` | `5000` |
| `dedup_retention` | `168h` |
| `dedup_timeout` | `2m` |

### `log`

| Option | Default | Environment |
|---|---|---|
| `level` | `info` | `KURWALL_LOG_LEVEL` |
| `format` | `json` | `KURWALL_LOG_FORMAT` |

---

## Agent (`/etc/kurwall/agent.yaml`)

The agent requires `APP_CONFIG_PATH` (the systemd unit sets
`/etc/kurwall/agent.yaml`). Unknown YAML keys are rejected.

### Top level

| Option | Default | Environment | Notes |
|---|---|---|---|
| `node_id` | certificate CN, else hostname | `KURWALL_NODE_ID` | Identity used for the agent certificate and dashboard |
| `coordinator_addr` | `127.0.0.1:9090` | `KURWALL_COORDINATOR_ADDR` | Coordinator gRPC address |

### `tls`

| Option | Default | Environment |
|---|---|---|
| `enabled` | `true` | — |
| `ca_file` | `/etc/kurwall/tls/ca.crt` | `KURWALL_TLS_CA_FILE` |
| `cert_file` | `/etc/kurwall/tls/agent.crt` | `KURWALL_TLS_CERT_FILE` |
| `key_file` | `/etc/kurwall/tls/agent.key` | `KURWALL_TLS_KEY_FILE` |
| `server_name` | `kurwall-coordinator` | `KURWALL_TLS_SERVER_NAME` |

`server_name` must match the coordinator certificate CN/SAN. The installer
generated by the coordinator writes `kurwall-coordinator`.

### `state`

| Option | Default | Environment |
|---|---|---|
| `sqlite_path` | `/var/lib/kurwall/agent.db` | `KURWALL_SQLITE_PATH` |

SQLite stores the outbox and the last applied policy snapshot.

### `inspection`

| Option | Default | Notes |
|---|---|---|
| `dns_enabled` | `true` | DNS inspection |
| `tls_enabled` | `true` | TLS ClientHello/SNI inspection |
| `tls_ports` | `[443]` | Ports queued for TLS inspection |
| `dns_queue_num` | `0` | NFQUEUE number; must differ from TLS queue |
| `tls_queue_num` | `1` | NFQUEUE number |
| `queue_max_len` | `2048` | At least `64` |
| `queue_max_packet` | `4096` | At least `512` |
| `dns_response_timeout` | `5s` | DNS verdict wait |
| `blocked_conn_ttl` | `5m` | How long blocked conntrack entries are remembered |
| `flow_snapshot_interval` | `1s` | Conntrack accounting snapshot; at least `200ms` |

### `firewall`

| Option | Default | Notes |
|---|---|---|
| `enabled` | `true` | nftables enforcement |
| `table_name` | `kurwall` | nftables table |
| `block_log_group` | `5` | NFLOG group for kernel drops |

### `docker`

| Option | Default | Notes |
|---|---|---|
| `enabled` | `true` | Container attribution |
| `socket` | `/var/run/docker.sock` | Docker socket |
| `refresh_interval` | `15s` | Container map refresh |

### `events`

| Option | Default | Notes |
|---|---|---|
| `batch_max_events` | `500` | 1–5000 |
| `flush_interval` | `1s` | Outbox flush interval |
| `max_outbox_batches` | `100000` | Drop-oldest cap |
| `retry_min` | `1s` | Reconnect retry floor |
| `retry_max` | `60s` | Reconnect retry ceiling |
| `send_timeout` | `30s` | Per-batch send timeout |
| `max_attempts` | `10` | Delivery attempts |
| `heartbeat_interval` | `15s` | Stream heartbeat |
| `reconnect_max` | `60s` | Maximum reconnect delay |

### `metrics`

| Option | Default | Environment |
|---|---|---|
| `addr` | `127.0.0.1:9877` | `KURWALL_METRICS_ADDR` |

Prometheus metrics and `/metrics` are exposed here. Keep it on loopback unless
you protect the port.

### `log`

| Option | Default | Environment |
|---|---|---|
| `level` | `info` | `KURWALL_LOG_LEVEL` |
| `format` | `json` | `KURWALL_LOG_FORMAT` |

---

## Compose / `.env` options

`deploy/.env` is consumed by Docker Compose. It is git-ignored; use
`deploy/.env.example` as the template.

| Variable | Default | Used by |
|---|---|---|
| `POSTGRES_USER` | `kurwall` | Postgres + coordinator |
| `POSTGRES_PASSWORD` | required | Postgres + coordinator |
| `POSTGRES_DB` | `kurwall` | Postgres + coordinator |
| `CLICKHOUSE_USER` | `kurwall` | ClickHouse + coordinator |
| `CLICKHOUSE_PASSWORD` | required | ClickHouse + coordinator |
| `CLICKHOUSE_DB` | `kurwall` | ClickHouse + coordinator |
| `KURWALL_API_TOKEN` | empty | Optional static admin bearer token |
| `KURWALL_HTTP_BIND_ADDR` | `127.0.0.1` | Host address for published 8080 |
| `KURWALL_GRPC_BIND_ADDR` | `0.0.0.0` | Host address for published 9090 |
| `KURWALL_BIND_ADDR` | unset | Fallback for both published ports; the standalone Compose file uses `0.0.0.0` when unset |
| `KURWALL_PUBLIC_URL` | dev `http://localhost:8080` | Install links, cookies |
| `KURWALL_GRPC_PUBLIC_ADDR` | derived | Address agents dial; may use a different host than `KURWALL_PUBLIC_URL` |
| `KURWALL_GRPC_SERVER_NAME` | `kurwall-coordinator` | TLS name agents verify for gRPC |
| `KURWALL_AGENT_DIST_DIR` | `/app/agent-dist` | Optional container path served at `/agent`; all images include the distribution |
| `KURWALL_TLS_SAN` | `DNS:kurwall-coordinator,DNS:localhost,IP:127.0.0.1` | SANs for the certificate generated by the standalone init container |
| `KURWALL_COORDINATOR_IMAGE` | `ghcr.io/nrf24l01/kurwall/coordinator:latest` | Prod compose image |
| `KURWALL_ASN_CATALOG_URL` | empty (iptoasn default) | Optional catalog mirror |
| `KURWALL_VERSION` | `dev` | Dev image build arg |
| `KURWALL_COMMIT` | `unknown` | Dev image build arg |
| `KURWALL_BUILD_DATE` | `unknown` | Dev image build arg |

Notes:

- `deploy/compose.standalone.yml` is the normal server deployment. It bakes the
  agent distribution into the coordinator image, generates the coordinator YAML
  and TLS certificates in an init container, and needs only `compose.yml` and
  `.env` on the host.
- All coordinator images include the agent distribution at `/app/agent-dist`.
  No host `agent-dist` mount is used.
- `KURWALL_PUBLIC_URL` is required by `compose.prod.yml` and
  `compose.standalone.yml`.
- `KURWALL_HTTP_BIND_ADDR` should stay on loopback behind a reverse proxy.
  `KURWALL_GRPC_BIND_ADDR` must be reachable by agents; restrict port `9090`
  with a host firewall when binding it to `0.0.0.0`.
- The image tag in `.env.example` should be changed to your registry/repository.
- The coordinator image includes the agent binary, systemd unit, sysctl file,
  manifest and checksums. `make agent-dist` is only needed for a custom or
  offline agent distribution.

---

## Installer options

The coordinator serves the installer at `/install.sh`.

| Option | Purpose |
|---|---|
| `--deploy-key KEY` | One-time enrollment key; alternative to supplying a certificate |
| `--cert PATH` | Install an existing agent certificate |
| `--key PATH` | Install an existing agent private key |
| `--node-id ID` | Set `node_id` in a newly generated config |
| `--force` | Rewrite the generated config and unit files |
| `--no-start` | Install files without starting/restarting the agent |

Environment: `KURWALL_NODE_ID` sets the node ID for a fresh config.
The installer requires root, and `openssl`/`tar` for deploy-key enrollment.

Offline installer:

```bash
./deploy/scripts/install-agent.sh <binary> <agent.yaml> <unit>
```

---

## Users CLI

Run inside the coordinator container:

```bash
docker compose -f deploy/compose.dev.yml exec coordinator \
  kurwall users create-admin --username admin

docker compose -f deploy/compose.dev.yml exec coordinator \
  kurwall users create --username viewer --password '...'

docker compose -f deploy/compose.dev.yml exec coordinator kurwall users list
docker compose -f deploy/compose.dev.yml exec coordinator \
  kurwall users reset-password --username admin
docker compose -f deploy/compose.dev.yml exec coordinator \
  kurwall users disable --username viewer
docker compose -f deploy/compose.dev.yml exec coordinator \
  kurwall users enable --username viewer
```

Passwords are at least 12 characters. `create` defaults to the `viewer` role.
Password reset and disable revoke existing sessions.

---

## Ports, paths and volumes

| Item | Default |
|---|---|
| HTTP/dashboard | `8080` |
| Agent gRPC | `9090` |
| Coordinator web assets | `/app/web` |
| Agent distribution | `/app/agent-dist` |
| Enrollment CA | `/var/lib/kurwall/enrollment` in the `kurwall-enrollment` volume |
| Coordinator config | `/config/coordinator.yaml` (standalone) or `/etc/kurwall/coordinator.yaml` (repo Compose) |
| Coordinator TLS | `/etc/kurwall/tls` |
| Agent config | `/etc/kurwall/agent.yaml` |
| Agent state | `/var/lib/kurwall/agent.db` |
| Compose volumes | `kurwall-postgres`, `kurwall-clickhouse`, `kurwall-enrollment`, `kurwall-config`, `kurwall-tls` |

---

## Example production snippets

`deploy/.env`:

```dotenv
POSTGRES_USER=kurwall
POSTGRES_PASSWORD=replace-with-a-long-random-password
POSTGRES_DB=kurwall

CLICKHOUSE_USER=kurwall
CLICKHOUSE_PASSWORD=replace-with-a-long-random-password
CLICKHOUSE_DB=kurwall

KURWALL_API_TOKEN=
KURWALL_HTTP_BIND_ADDR=127.0.0.1
KURWALL_GRPC_BIND_ADDR=0.0.0.0
KURWALL_PUBLIC_URL=https://kurwall.example.com
KURWALL_GRPC_PUBLIC_ADDR=grpc.kurwall.example.com:9090
KURWALL_GRPC_SERVER_NAME=kurwall-coordinator
KURWALL_TLS_SAN=DNS:kurwall-coordinator,DNS:localhost,IP:127.0.0.1,IP:192.0.2.10
KURWALL_COORDINATOR_IMAGE=ghcr.io/nrf24l01/kurwall/coordinator:latest
# Optional catalog mirror if iptoasn is slow:
# KURWALL_ASN_CATALOG_URL=http://www.db.mirror.stargrave.org/ip/ip2asn-combined.tsv.gz
```

Coordinator server/TLS essentials:

```yaml
server:
  public_url: "https://kurwall.example.com"
  grpc_public_addr: "grpc.kurwall.example.com:9090"
  grpc_server_name: "kurwall-coordinator"
  agent_dist_dir: "/app/agent-dist"
  enrollment_dir: "/var/lib/kurwall/enrollment"

tls:
  enabled: true
  ca_file: /etc/kurwall/tls/ca.crt
  cert_file: /etc/kurwall/tls/coordinator.crt
  key_file: /etc/kurwall/tls/coordinator.key
  require_client_cert: true
```

Agent essentials:

```yaml
node_id: ""
coordinator_addr: "kurwall.example.com:9090"
tls:
  enabled: true
  ca_file: /etc/kurwall/tls/ca.crt
  cert_file: /etc/kurwall/tls/agent.crt
  key_file: /etc/kurwall/tls/agent.key
  server_name: "kurwall-coordinator"
```