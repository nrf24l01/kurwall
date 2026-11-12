# Kurwall

Kurwall monitors, records and filters outgoing network traffic on Linux hosts
and Docker containers. A central coordinator distributes policies and stores
events; a privileged node agent enforces filtering locally and keeps working
when the coordinator is unavailable.

```
                    mTLS gRPC (events + policy snapshots)
  ┌────────────┐  ────────────────────────────────────────►  ┌──────────────┐
  │   Agent    │                                             │ Coordinator  │
  │  (node)    │  ◄────────────────────────────────────────  │   (Go)       │
  └─────┬──────┘                                             └──────┬───────┘
        │ nftables / NFQUEUE / conntrack                            │
        │                                                           ├── PostgreSQL
  ┌─────▼──────┐                                              ┌─────▼──────┐
  │ Linux host │                                              │ ClickHouse │
  │ + Docker   │                                              └────────────┘
  └────────────┘                                                     ▲
                                                              ┌──────┴───────┐
                                                              │ Vue dashboard │
                                                              └──────────────┘
```

## Components

| Component | Path | Responsibility |
| --- | --- | --- |
| Coordinator | `cmd/coordinator`, `internal/coordinator` | REST API, gRPC ingest, policy revisions, PostgreSQL, ClickHouse, embedded dashboard |
| Agent | `cmd/agent`, `internal/agent` | nftables enforcement, DNS/SNI inspection, conntrack flow accounting, Docker attribution, SQLite outbox |
| Dashboard | `web` | Vue 3 + Rosé Pine console UI |

### Agent pipeline

1. nftables matches blocked IPv4/IPv6 addresses and CIDR ranges in the
   `OUTPUT` and `FORWARD` hooks. Drops are logged through NFLOG so the agent can
   attribute them to rules.
2. `FORWARD` also covers Docker bridge traffic; the Docker Engine socket maps
   source addresses to container identities.
3. DNS packets are queued to NFQUEUE. Queries are recorded, A/AAAA answers are
   cached as IP→domain associations, and blocked UDP queries are rewritten into
   NXDOMAIN responses.
4. TLS packets on the configured ports are queued to a second NFQUEUE queue
   until the agent has inspected the first payload. The agent reassembles TCP
   segments, parses the ClientHello SNI (no decryption), blocks matching domains
   and then marks the conntrack entry so later packets bypass inspection.
5. conntrack produces one consolidated record per completed connection with
   final byte/packet counters, duration, container, domain/SNI and decision.
   Closing TCP states are detected from periodic accounting snapshots so records
   appear promptly instead of waiting for the TIME_WAIT entry to expire.
6. Events are batched and written to a durable SQLite outbox, then delivered
   over the persistent gRPC stream. Unacknowledged batches are retried after
   reconnects; the coordinator deduplicates by batch ID.

Filtering is allow-by-default. Encrypted DNS (DoH/DoT), QUIC/HTTP3, ECH, and
fragmented or otherwise unidentifiable destinations remain allowed unless an
IP/CIDR rule matches — domain filtering is explicitly best-effort. DNS over TCP
is recorded but cannot be rewritten into NXDOMAIN; TLS SNI enforcement still
applies to destinations reached over TCP DNS.

## Quick start

Requirements: Docker (or rootless Podman with the Compose provider), Go 1.27+,
Bun 1.4+ for local frontend development, and Linux 5.15+ with nftables,
conntrack, NFQUEUE and NFLOG on agent hosts.

```bash
cp deploy/.env.example deploy/.env
# edit passwords and KURWALL_BIND_ADDR

# Create CA + coordinator + agent certificates.
NODE_ID=edge-1 \
COORDINATOR_SAN="DNS:kurwall-coordinator,DNS:localhost,IP:127.0.0.1,IP:10.0.0.10" \
  ./deploy/scripts/gen-certs.sh deploy/tls

cd deploy
docker compose -f compose.dev.yml up -d --build
curl -fsS http://127.0.0.1:8080/ping

# Create the first administrator on the coordinator (prompts for a password twice).
docker compose -f compose.dev.yml exec coordinator \
  kurwall users create-admin --username admin
```

The dashboard is served by the coordinator at `http://127.0.0.1:8080` and asks
for a sign-in as soon as any user account exists. Accounts are managed with
`docker compose exec coordinator kurwall users ...`:
`create-admin`, `create` (viewer), `list`, `reset-password`, `disable` and
`enable`. Passwords must be at least 12 characters.
`KURWALL_BIND_ADDR` may be a private or VPN address so agents can reach gRPC
port 9090. On a rootless host, the same files work with `podman compose` after
`systemctl --user enable --now podman.socket`.

### Agent installation

Publish the agent distribution once on the coordinator host:

```bash
# Static musl build with SQLite; also writes the manifest and checksums.
make agent-dist
cd deploy
docker compose -f compose.dev.yml up -d --build
```

Then sign in to the dashboard, open **Nodes**, and choose **Install node**.
Enter a node name, optionally assign policies, and create a one-time install
link. The link contains a deploy key:

```bash
curl -fsSL 'http://coordinator.example.com:8080/install.sh?key=…' | sudo bash
```

On first use the installer generates the private key on the node, exchanges the
deploy key for a certificate signed by the coordinator's enrollment CA, writes
`/etc/kurwall/agent.yaml`, verifies the binary SHA-256, installs the systemd
unit and sysctl tuning, and starts the agent. The deploy key is single-use and
expires (default 1 hour). The node appears immediately and receives the policies
selected on the key.

Re-running an install link is an update: it keeps `/etc/kurwall/agent.yaml` and
the node certificate. The installer may also be run without a key when
`agent.crt`/`agent.key` already exist. The public assets are `GET /install.sh`
and `GET /agent/*` (versions, checksums, manifest, CA, unit, sysctl, binaries).

The offline installer remains available for air-gapped hosts:

```bash
./deploy/scripts/build-agent-static.sh build/kurwall-agent
# On the node, after copying the binary, certificates and config:
./deploy/scripts/install-agent.sh build/kurwall-agent deploy/agent.yaml
```

The agent needs `CAP_NET_ADMIN` and reads the host network namespace. The
systemd unit grants exactly that capability. The nftables table `kurwall` is
left in place across agent restarts, so blocking survives a daemon restart.

Agent certificates use their Common Name as node identity. The coordinator
auto-registers an unknown CN on first connect; the mTLS CA is the trust
boundary.

The install script also applies `net.netfilter.nf_conntrack_acct=1` and
`nf_conntrack_timestamp=1`, and loads `nf_conntrack_netlink`, `nfnetlink_queue`
and `nfnetlink_log`. Byte/packet counters come from periodic conntrack
accounting snapshots (`inspection.flow_snapshot_interval`, default 1s).

## Configuration

Both binaries require `APP_CONFIG_PATH` (or `--config`). YAML is decoded
strictly and environment variables override deployment values.

Coordinator secrets: `POSTGRES_PASSWORD`, `CLICKHOUSE_PASSWORD`,
`KURWALL_API_TOKEN`, TLS file paths.

Agent secrets/overrides: `KURWALL_COORDINATOR_ADDR`, `KURWALL_NODE_ID`,
`KURWALL_SQLITE_PATH`, `KURWALL_TLS_*`, `KURWALL_METRICS_ADDR`.

Node deployment configuration (`server` in `coordinator.yaml`):

```yaml
server:
  public_url: "https://kurwall.example.com"   # KURWALL_PUBLIC_URL
  grpc_public_addr: "kurwall.example.com:9090" # optional, derived otherwise
  agent_dist_dir: "/app/agent-dist"            # KURWALL_AGENT_DIST_DIR
  enrollment_dir: "/var/lib/kurwall/enrollment" # KURWALL_ENROLLMENT_DIR
```

The enrollment CA is generated on first start inside `enrollment_dir` and is
kept in the `kurwall-enrollment` volume. It signs node certificates issued
through one-time deploy keys. It is deliberately separate from the main agent
CA: compromising the coordinator does not compromise the main CA. The gRPC
listener trusts both the main CA and the enrollment CA, so existing manually
issued node certificates keep working. Node certificates are valid for one
year; re-enrollment currently requires a new deploy key and a node name that is
not already registered.

Back up the `kurwall-enrollment` volume together with the PostgreSQL volume.
If it is lost, a new enrollment CA is generated and previously auto-enrolled
nodes are no longer trusted; nodes with manually issued main-CA certificates
continue to connect.

ASN resolution configuration (`asn` in `coordinator.yaml`):

```yaml
asn:
  enabled: true
  prefix_url_template: "https://stat.ripe.net/data/announced-prefixes/data.json?resource=AS{asn}"
  holder_url_template: "https://stat.ripe.net/data/as-overview/data.json?resource=AS{asn}"
  refresh_interval: 12h
  retry_interval: 5m
  request_timeout: 10s
  max_prefixes: 20000
  catalog_enabled: true
  catalog_url: "https://iptoasn.com/data/ip2asn-combined.tsv.gz"
  catalog_refresh_interval: 168h
  catalog_retry_interval: 5m
  catalog_timeout: 90s
```

The coordinator needs outbound HTTPS access to the configured sources. A rule
that resolves to more than `max_prefixes` is rejected so a single rule cannot
produce an unbounded nftables set.

`KURWALL_ASN_CATALOG_URL` (or `asn.catalog_url`) can point at a mirror of the
iptoasn combined feed when the default iptoasn host is slow or unreachable from
your network. The mirror is third-party display data; prefer an HTTPS mirror
when one is available. The default remains the HTTPS iptoasn URL.

Useful defaults:

- REST `:8080`, gRPC `:9090`, dashboard served from `/app/web`.
- TLS 1.3 with required client certificates.
- ClickHouse ZSTD compression and 30-day TTL on all event tables.
- Agent inspection queues: DNS `0`, TLS `1`; TLS ports `[443]`.
- Agent outbox flush every second / 500 events.
- Agent conntrack accounting snapshot every second.

## Policy model

A policy contains IP, CIDR, domain and ASN rules with action `block`. Policies
are assigned to nodes; every mutation creates a monotonic revision and pushes a
node-specific snapshot to connected agents. The policy list and detail view show
the assigned nodes, their connectivity, agent version and applied revision. Agents cache the last applied
snapshot in SQLite and apply it at startup without contacting the coordinator.

ASN rules (for example `AS13335`) are resolved by the coordinator into the
currently announced prefixes and cached in PostgreSQL. A node snapshot carries
one ASN rule with its prefix list; the agent indexes every prefix and attributes
a kernel drop back to the ASN rule. The coordinator refreshes due ASN entries in
the background (default every 12 hours, retrying failures every 5 minutes) and
publishes a new revision when the prefix set changes. The cache keeps working
when the BGP source is unavailable; a new ASN rule is rejected if it cannot be
resolved at all. The default source is RIPEstat and the URL templates can point
at a self-hosted BGP dataset.

The **Autonomous systems** page lists the ASN directory, filters by country and
searches by ASN or holder name, and adds an ASN rule to a selected policy in one
step. The directory is built from the configurable iptoasn table (ASN holder,
country and routed prefixes deduplicated by ASN) and cached in PostgreSQL. The
catalog is refreshed weekly and retried every few minutes if unavailable, so the
page remains usable without a live download. Search is live (debounced), and
each row carries its country flag and a neutral company mark. Rows can be
selected individually, with Shift-click, or by dragging across the table; the
selection is kept across searches and pages within the page view. "Block
selected" creates the ASN rules in one policy revision, skips duplicates, and
reports ASNs whose prefixes could not be resolved.

The dashboard also subscribes to a WebSocket stream for live traffic and node
status. The browser requests a 30-second ticket from
`POST /api/v1/stream-ticket`, then connects to `GET /api/v1/stream?ticket=…`.
Frames carry persisted events and node status changes. Live delivery is
best-effort; ClickHouse remains the complete history. Overview shows the last
live frames and Events can prepend new matching rows while its Live chip is on.

The Events → Traffic filter builder is persisted in the browser (`localStorage`)
across reloads: the date-time window, node, condition rows, match mode,
exclude-local and live state. The builder also exposes a **Wireshark filter** input that accepts expressions
such as `ip.dst == 8.8.8.8 && !domain contains "ads"` and mirrors them into the
condition rows. Press Enter or Apply to parse it. Use one connector type per
expression (`&&` or `||`); mixed connectors and parentheses are not supported by
the row model.

Condition rows use Wireshark-style fields such as
`text`, `ip.addr`, `ip.src`, `ip.dst`, `port`, `src.port`, `dst.port`, `protocol`,
`domain`, `sni`, `target`, `event.kind`, `decision`, `block.kind`, `container`,
`bytes.out`, `bytes.in`, `duration` and `reason`, with operators `is`, `is not`,
`contains`, `does not contain`, `starts with`, `ends with`, `greater than`,
`at least`, `less than` and `at most`. Rows combine with **Match all** (AND) or
**Match any** (OR); the `!` button negates a row. The API accepts the rows as a
JSON `filter` query parameter and a `match=all|any` parameter; values are
validated and parameterized server-side.

Domain semantics:

- `example.com` blocks the domain and every subdomain.
- `*.example.com` blocks subdomains only.
- Values are lowercased and IDN-normalized.
- Blocked DNS queries receive NXDOMAIN; TLS connections are dropped at the SNI
  layer. IP rules are enforced by nftables for all protocols.

The full effective rule set of a node is visible at
`GET /api/v1/nodes/{id}/snapshot`.

## API

All responses use one envelope:

```json
{
  "status": 200,
  "code": "OK",
  "message": "success",
  "traceId": "…",
  "timestamp": "…",
  "path": "/api/v1/nodes",
  "data": {}
}
```

| Method and path | Purpose |
| --- | --- |
| `GET /ping` | Liveness |
| `GET /api/v1/health` | PostgreSQL + ClickHouse readiness |
| `GET /api/v1/meta` | Build, auth mode, public URL and agent defaults |
| `POST /api/v1/auth/login` | Start a browser session |
| `GET /api/v1/auth/me` | Current user and authentication mode |
| `POST /api/v1/auth/logout` | End the browser session |
| `POST /api/v1/deploy-keys` | Create a one-time node install link (admin) |
| `GET/DELETE /api/v1/deploy-keys[/{id}]` | List or revoke deploy keys (admin) |
| `POST /api/v1/enroll` | Exchange a deploy key and CSR for a node certificate |
| `GET /install.sh` | Public node installer/updater |
| `GET /agent/*` | Public agent binaries, manifest, checksums, CA and unit |
| `GET /api/v1/nodes` | List nodes |
| `GET/PATCH /api/v1/nodes/{id}` | Read / rename / relabel |
| `PUT /api/v1/nodes/{id}/policies` | Replace policy assignments |
| `GET /api/v1/nodes/{id}/snapshot` | Effective policy |
| `GET/POST /api/v1/policies` | List / create |
| `GET/PATCH/DELETE /api/v1/policies/{id}` | Policy detail and mutation |
| `GET/POST /api/v1/policies/{id}/rules` | List / create rules |
| `PATCH/DELETE /api/v1/policies/{id}/rules/{ruleId}` | Rule mutation |
| `GET /api/v1/events/flows` | Connection history |
| `GET /api/v1/events/dns` | DNS history |
| `GET /api/v1/events/blocks` | Blocking events |
| `GET /api/v1/events/traffic` | Unified allowed/blocked feed (`nodeId`, `from`, `to`, `decision`, `domain`, `ip`, `srcIp`, `dstIp`, `container`, `protocol`, `excludeLocal`, `filter`, `match`, `limit`, `offset`) |
| `GET /api/v1/stats/overview` | Dashboard counters |
| `POST /api/v1/stream-ticket` | Issue a short-lived live-stream ticket |
| `GET /api/v1/stream?ticket=…` | WebSocket live traffic and node status |
| `GET /api/v1/asns` | Search the ASN directory by `country` and `q` |
| `POST /api/v1/policies/:id/rules/batch` | Add several rules in one revision |
| `GET /api/v1/asns/countries` | Country facet with ASN counts |
| `GET /api/v1/asns/{asn}` | Resolve an ASN and preview its prefixes |

Filters include `nodeId`, `from`, `to`, `limit`, `offset`, `domain`,
`container`, `srcIp`/`dstIp`, `decision`, `blocked`, and `kind` (`ip`, `cidr`,
`domain`, `asn`, `dns`). Time values are RFC3339; the maximum query window is
90 days.

When at least one user exists, dashboard/API requests need a session cookie from
`POST /api/v1/auth/login`. State-changing session requests must also send the
`X-CSRF-Token` value from the `kurwall_csrf` cookie. A configured
`auth.api_token` remains available as an administrator bearer token for
automation. When no users and no token exist, the API is open.

## Storage

**PostgreSQL** — nodes, policies, rules, node assignments, immutable policy
revisions, event-batch idempotency records, the ASN prefix cache, the
searchable ASN directory, and the ASN range catalog (`asn_ranges`) used to
enrich traffic events with destination ASN and country. Goose migrations run at
startup and are embedded in the binary.

**ClickHouse** — `flows`, `dns_events`, `block_events`. Typed columns use
`LowCardinality`, `T64`, `DoubleDelta` and ZSTD codecs; tables are partitioned by
day and TTL-deleted after 30 days. Inserts are batched per agent batch. A batch
claim in PostgreSQL prevents duplicate ingestion in the normal retry path. The
traffic API unions `flows` and `block_events` in one query and enriches the
result with destination ASN and country from `asn_ranges`.

**SQLite (agent)** — WAL-mode outbox of encoded protobuf batches and the last
applied policy snapshot. At 15 events/s the default outbox cap (100k batches)
provides days of coordinator downtime; when full, the oldest events are dropped
with a loud error log and a Prometheus counter. Blocking and inspection continue
while the coordinator is unreachable; unacknowledged batches are retried after
reconnect and deduplicated by batch ID. Events are batched for at most one flush
interval before reaching SQLite, so a graceful stop flushes them while an abrupt
kill can lose at most the current interval. A coordinator crash in the narrow
window between a ClickHouse insert and the batch-commit record can produce
duplicates; the normal retry path is idempotent.

## Observability

The agent exposes Prometheus metrics on `metrics.addr` (default
`127.0.0.1:9877`) at `/metrics`:

- `kurwall_agent_events_recorded_total{kind}`, `..._dropped_total{kind}`
- `kurwall_agent_outbox_batches`, `kurwall_agent_outbox_events`
- `kurwall_agent_coordinator_connected`, `..._grpc_reconnects_total`
- `kurwall_agent_policy_version`, `..._policy_applied_timestamp_seconds`
- `kurwall_agent_flows_total`, `..._flow_bytes_total{direction}`
- `kurwall_agent_blocks_total{kind}`, `..._dns_queries_total`
- `kurwall_agent_nfqueue_errors_total{inspector}`, `..._nflog_errors_total`

Coordinator logs are JSON with trace IDs; every HTTP response carries
`X-Trace-ID`.

### Troubleshooting

```bash
nft list table inet kurwall              # rules and sets
conntrack -L | head                     # active flows
ss -lunp | grep 9877                    # agent metrics
journalctl -u kurwall-agent -f          # agent logs
sqlite3 /var/lib/kurwall/agent.db 'select count(*), sum(event_count) from outbox;'
```

If SNI blocking appears to miss traffic, verify that nftables queue 1 shows
increments (`nft list chain inet kurwall output`) and that the agent log has no
NFQUEUE errors. ECH and QUIC are intentionally best-effort. If Docker
attribution is empty, verify the agent can read `/var/run/docker.sock`.

For ASN rules, check `GET /api/v1/asns/AS13335` and the coordinator logs. The
rules use the cached prefix set while the resolver is down; a fresh coordinator
without a cache needs the configured source to create a new ASN rule.

When the agent daemon is stopped, the `kurwall` nftables table is intentionally
left in place: IP/CIDR drops keep working, but domain inspection requires the
userspace queue and therefore stops until the agent returns.

## Security notes

- mTLS is the agent trust boundary; keep the CA key offline.
- The dashboard API should be published only on localhost or a private/VPN
  address; optionally enable `KURWALL_API_TOKEN`.
- User accounts live in PostgreSQL with bcrypt password hashes. Roles are
  `admin` and `viewer`; all mutations require `admin`. Sessions are server-side,
  HttpOnly and expire after 12 hours.
- Login attempts are rate-limited per IP and username. Session changes require
  a double-submitted CSRF token. Set `KURWALL_PUBLIC_URL` to an HTTPS origin so
  session cookies are marked `Secure`.
- Deploy keys are hashed at rest, single-use and time-limited. The enrollment CA
  is separate from the main agent CA and lives in its own volume.
- `/install.sh`, `/agent/*` and `/api/v1/enroll` are intentionally unauthenticated
  so nodes can bootstrap. They expose only the installer, public CA, agent
  binaries and certificate enrollment.
- Agent containers are not used in production: run the daemon on the host with
  only `CAP_NET_ADMIN`.
- The TLS inspector does not decrypt traffic. The agent reserves conntrack mark
  bits `0x10000000` (inspected) and `0x20000000` (blocked).
- For production certificate handling, mount the coordinator key through Docker
  secrets instead of a world-readable bind mount.

## Dashboard console

The dashboard follows the Rosé Pine console language: dark only, IBM Plex
Mono (bundled locally), one type scale, colour only for state, flush blocks
separated by 1 px `--overlay` lines, a 212 px sidebar, a 48 px header, a 28 px
key line and 36 px area title bands. Tokens and shared primitives live in
`web/src/styles/tokens.css` and `web/src/styles/app.css`; console components
are in `web/src/components/console/`. The app icon is the black square with a
light square half its width.

Status always pairs a shape and a word: pine dot for healthy, gold triangle for
degraded, iris diamond for blocked, love square for broken, ring for unknown,
open square for disabled, bars for paused, pulse for running and a dash for
gone. Actions are boxed; the primary action is the light button. The interface
is keyboard-first on desktop (`/` search, `:` actions, `g` then a page letter,
`?` keys, `r` refresh, `i` first field, `j k` list rows, `↵` open, `esc` back)
and a touch layout under 760 px (drawer sidebar, 56 px header, 48 px targets,
16 px fields, no key line).

## Development

```bash
make test          # Go unit tests (policy matcher, DNS rewrite, SNI parser, firewall)
make build         # web + coordinator + agent
cd web && bun run dev   # Vite dev server on :5173, proxies /api to :8080
```

GitHub Actions runs formatting, vet, Go tests, the static agent build, the
dashboard type-check/build and publishes the coordinator image to GHCR on
`main` (`.github/workflows/ci.yml`).

```
cmd/                  entrypoints
internal/agent/       firewall, dns, tlsinspect, flow, docker, policy, events, transport
internal/coordinator/ config, httpapi, grpcapi, service, storage, domain
internal/platform/    shared logging, errors, rule matching, TLS, version
proto/                protobuf definitions
migrations/           embedded PostgreSQL and ClickHouse schemas
deploy/               Dockerfiles, Compose stacks, agent install assets
web/                  Vue 3 Rosé Pine console dashboard
```