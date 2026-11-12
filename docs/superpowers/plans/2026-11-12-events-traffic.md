# Unified Traffic Events Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Connections/Blocks tabs with one Traffic table showing allowed connections and blocking decisions, separate source/destination columns, blocked-row colouring, and destination ASN/country.

**Architecture:** A new ClickHouse `UNION ALL` query returns allowed `flows` plus all `block_events`; the coordinator enriches destination IPs with an IP→ASN range table populated from the existing iptoasn catalog. History is enriched at read time and live frames before publish. The dashboard adds a Traffic tab, keeps DNS separate, and uses the new favicon as a larger header/login brand mark.

**Tech Stack:** Go, Echo, PostgreSQL, ClickHouse, Vue 3, TypeScript, Bun, ImageMagick.

**Spec:** `docs/superpowers/specs/2026-11-12-events-traffic-design.md`

## Global Constraints

- Show one row per blocking decision; IP/CIDR/ASN drops are deduplicated by the agent within 10 seconds.
- Exclude `flows.blocked = 1` from Traffic; `block_events` is authoritative.
- ASN country is the ASN holder's registered country, not IP geolocation.
- Support IPv4 and IPv6 ranges from the iptoasn combined feed.
- Do not change ClickHouse tables, retention, the agent protobuf, or the existing `/events/flows`, `/events/dns`, `/events/blocks` endpoints.
- Do not let ASN rule resolution being disabled disable the catalog/range load.
- New API route is authenticated and read-only; no new role requirements.
- Keep the Rosé Pine console language: blocked uses iris state colour, flush 1 px lines, no gradients.

## Review Focus

- ASN ranges empty or catalog refresh failing: Traffic still renders, ASN/country show `—`, and live ingestion succeeds.
- IPv6 and private destinations: lookup returns no match, UI shows `—`, no error or panic.
- Live frames before range data exists: no ASN fields, UI still renders.
- Blocked flows: excluded so blocked rows are not duplicated.
- Union pagination and totals: allowed flows + block decisions only, stable order by time/event.
- Zero ports and empty target/domain: source/destination cells do not render `:0` or `—:`.
- Blocked row tint: readable against the existing hover/selected states.

---

### Task 1: ASN ranges and IP lookup

**Files:**
- Create: `migrations/postgres/00005_asn_ranges.sql`
- Modify: `internal/coordinator/asn/catalog.go`
- Modify: `internal/coordinator/asn/catalog_test.go`
- Modify: `internal/coordinator/domain/model.go`
- Modify: `internal/coordinator/domain/repository.go`
- Modify: `internal/coordinator/store/postgres/asn_catalog.go`
- Modify: `internal/coordinator/service/asn.go`
- Modify: `internal/coordinator/config/validate.go`
- Modify: `internal/coordinator/app/app.go`

**Interfaces:**
- Produces: `asn.CatalogResult{Entries []CatalogEntry; Ranges []CatalogRange}`, `Catalog.Load(ctx) (CatalogResult, error)`, `CatalogRange{Start, End netip.Addr; ASN int64; Country, Name string}`.
- Produces: `domain.ASNRange{StartIP, EndIP string; ASN int64; Country, Name string}`, `domain.ASNInfo{ASN int64; Country, Name string}`.
- Produces: `ReplaceASNCatalog(ctx, entries []ASNEntry, ranges []ASNRange) error`, `CountASNRanges(ctx) (int64, error)`, `LookupASNForIPs(ctx, ips []string) (map[string]ASNInfo, error)`.
- `LookupASNForIPs` keys are normalized `addr.Unmap().String()` values; missing/IPv6 gaps are absent from the map.

- [ ] **Step 1: Extend the catalog test fixture and assertions**

Add IPv6 rows and assert both entries and ranges. The test should expect:
- Entries: AS13335 (US, CLOUDFLARENET), AS42 (NO, WOODYNET).
- Ranges include `1.0.0.0–1.0.0.255`, `1.0.4.0–1.0.7.255`, `3.0.0.0–3.0.0.255`, and `2001:4860::/32` parsed as a range.

```go
result, err := catalog.Load(t.Context())
if err != nil { t.Fatal(err) }
if len(result.Entries) != 2 { ... }
if len(result.Ranges) != 4 { ... }
// assert IPv4 and IPv6 range boundaries and the 42/NO range survived
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/coordinator/asn -run TestCatalogLoad -v`
Expected: compile failure or assertion failure because `Load` still returns `[]CatalogEntry`.

- [ ] **Step 3: Implement range parsing**

In `catalog.go`, add:

```go
type CatalogRange struct {
    Start, End netip.Addr
    ASN        int64
    Country    string
    Name       string
}

type CatalogResult struct {
    Entries []CatalogEntry
    Ranges  []CatalogRange
}

func (c *Catalog) Load(ctx context.Context) (CatalogResult, error)
```

For each valid row (same ASN/country/name filters as today), append a range after parsing `fields[0]` and `fields[1]` with `netip.ParseAddr`. Skip invalid, unspecified, or reversed ranges. Keep collecting the deduplicated `CatalogEntry` map. Return `CatalogResult` with both slices.

- [ ] **Step 4: Run the catalog test**

Run: `go test ./internal/coordinator/asn -run TestCatalogLoad -v`
Expected: PASS.

- [ ] **Step 5: Add the migration and domain/repository types**

`00005_asn_ranges.sql`:

```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS asn_ranges (
    start_ip inet NOT NULL,
    end_ip   inet NOT NULL,
    asn      bigint NOT NULL,
    country  text NOT NULL DEFAULT '',
    name     text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS asn_ranges_start_idx ON asn_ranges (start_ip);

-- +goose Down
DROP TABLE IF EXISTS asn_ranges;
```

Add `ASNRange`/`ASNInfo` to `domain/model.go`. Change `Repository.ReplaceASNCatalog` to take both slices and add `CountASNRanges` and `LookupASNForIPs` to the interface.

- [ ] **Step 6: Implement the store methods**

In `store/postgres/asn_catalog.go`:

- `ReplaceASNCatalog` truncates `asn_catalog` and `asn_ranges`, copies both, commits. Refuse empty entries; ranges may be empty only if the source had none.
- `CountASNRanges` returns `count(*)`.
- `LookupASNForIPs` normalizes inputs, returns `map[string]domain.ASNInfo`, and uses:

```sql
SELECT host(ip), COALESCE(r.asn, 0), COALESCE(r.country, ''), COALESCE(r.name, '')
FROM unnest($1::inet[]) AS ip
LEFT JOIN LATERAL (
    SELECT asn, country, name, end_ip
    FROM asn_ranges
    WHERE start_ip <= ip
    ORDER BY start_ip DESC
    LIMIT 1
) r ON r.end_ip >= ip
```

- [ ] **Step 7: Wire the service, config and app**

In `service/asn.go`, map `CatalogResult` to domain entries/ranges and call `ReplaceASNCatalog`. In `refreshCatalog`, load ranges count and reload when `rangeCount == 0` even if the directory timestamp is fresh.

In `config/validate.go`, validate catalog URL/intervals whenever `catalog_enabled`, independently of `asn.enabled`.

In `app/app.go`, construct `asnCatalog` when `cfg.ASN.CatalogEnabled` (not nested under `cfg.ASN.Enabled`); construct the resolver only when `cfg.ASN.Enabled`.

- [ ] **Step 8: Build and run backend tests**

Run: `go build ./... && go test ./internal/coordinator/asn ./internal/coordinator/config ./internal/coordinator/service`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add migrations/postgres/00005_asn_ranges.sql internal/coordinator/asn internal/coordinator/domain internal/coordinator/store/postgres/asn_catalog.go internal/coordinator/service/asn.go internal/coordinator/config/validate.go internal/coordinator/app/app.go
git commit -m "feat: store ASN ranges for destination lookup"
```

---

### Task 2: Traffic domain type, ClickHouse query and API

**Files:**
- Modify: `internal/coordinator/domain/repository.go`
- Modify: `internal/coordinator/store/clickhouse/query.go`
- Create: `internal/coordinator/store/clickhouse/query_test.go`
- Modify: `internal/coordinator/service/events.go`
- Modify: `internal/coordinator/httpapi/handler/events.go`
- Modify: `internal/coordinator/httpapi/router.go`

**Interfaces:**
- Produces: `domain.TrafficEvent` with the JSON fields in the spec, `domain.TrafficQuery`.
- Produces: `EventReader.Traffic(ctx, TrafficQuery) ([]TrafficEvent, uint64, error)`.
- Produces: `Events.Traffic(ctx, TrafficQuery) (domain.Page[domain.TrafficEvent], error)`.
- Produces: `GET /api/v1/events/traffic`.
- `Events.Traffic` enriches `DstASN`/`DstCountry` via `repo.LookupASNForIPs`; lookup failures are ignored.

- [ ] **Step 1: Write the branch-selection test**

In `query_test.go`:

```go
func TestTrafficBranches(t *testing.T) {
    cases := []struct{ decision string; flows, blocks bool }{
        {"", true, true},
        {"allowed", true, false},
        {"blocked", false, true},
    }
    for _, tc := range cases {
        flows, blocks := trafficBranches(tc.decision)
        if flows != tc.flows || blocks != tc.blocks { ... }
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/coordinator/store/clickhouse -run TestTrafficBranches -v`
Expected: compile failure because `trafficBranches` does not exist.

- [ ] **Step 3: Add domain types and implement `Traffic`**

Add `TrafficEvent`, `TrafficQuery`, and the reader method. Implement `trafficBranches(decision string) (bool, bool)` (empty/all → both; allowed → flows; blocked → blocks).

Implement `Store.Traffic`:
- Count the included branches with two `count()` queries.
- Build a union with explicit casts and aliases:

```sql
SELECT * FROM (
  SELECT event_id, 'connection' AS kind, node_id, started_at AS at, ended_at,
         duration_ms, src_ip, dst_ip, src_port, dst_port, protocol,
         domain, sni, '' AS target, '' AS block_kind,
         container_id, container_name, bytes_in, bytes_out, packets_in, packets_out,
         false AS blocked, rule_id, reason
  FROM flows WHERE blocked = 0 AND <filters>
  UNION ALL
  SELECT event_id, 'block', node_id, at, at,
         toUInt32(0), src_ip, dst_ip, src_port, dst_port, protocol,
         domain, '', target, kind,
         container_id, container_name, toUInt64(0), toUInt64(0), toUInt64(0), toUInt64(0),
         true, rule_id, reason
  FROM block_events WHERE <filters>
)
ORDER BY at DESC, event_id DESC
LIMIT <n> OFFSET <m>
```

- Filters: node, time, protocol, container, `ip` matching either side, `srcIp`/`dstIp`, and domain/target search. For flows the search covers `domain`/`sni`; for blocks it covers `domain`/`target`.
- Scan into `TrafficEvent`, format IPs with `formatRawIP`, and return the page total.

- [ ] **Step 4: Run the test and build**

Run: `go test ./internal/coordinator/store/clickhouse -run TestTrafficBranches -v && go build ./...`
Expected: PASS.

- [ ] **Step 5: Add the service method and enrichment**

Add `Events.Traffic`: normalize the window, call `reader.Traffic`, collect unique `DstIP` values, call `repo.LookupASNForIPs` once, set `DstASN`/`DstCountry`, and return `domain.Page`. Ignore lookup errors.

- [ ] **Step 6: Add the handler and route**

Add `ListTraffic` parsing `from`, `to`, `limit`, `offset`, `protocol`, `decision` (empty/allowed/blocked), `nodeId`, `srcIp`, `dstIp`, `ip`, `domain`, `container`. Validate decision like `ListFlows`. Register `api.GET("/events/traffic", h.ListTraffic)`.

- [ ] **Step 7: Run tests and build**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/coordinator/domain/repository.go internal/coordinator/store/clickhouse internal/coordinator/service/events.go internal/coordinator/httpapi/handler/events.go internal/coordinator/httpapi/router.go
git commit -m "feat: add unified traffic events API"
```

---

### Task 3: Enrich live event frames

**Files:**
- Modify: `internal/coordinator/domain/repository.go`
- Modify: `internal/coordinator/service/events.go`

**Interfaces:**
- Produces: `FlowRecord.DstASN int64` / `DstCountry string` and the same on `BlockRecord`, serialized as `dstAsn`/`dstCountry`.
- Produces: `Events.Ingest` enriches flow and block records after insert and before `PublishEvents`.

- [ ] **Step 1: Add the JSON fields**

Add optional fields to `FlowRecord` and `BlockRecord`:

```go
DstASN     int64  `json:"dstAsn,omitempty"`
DstCountry string `json:"dstCountry,omitempty"`
```

They are not persisted in ClickHouse; the insert mapper ignores them.

- [ ] **Step 2: Enrich before live publish**

In `Ingest`, after `writer.InsertBatch` and before `publisher.PublishEvents`, collect destination IPs from `ev.Flow` and `ev.Block`, call `repo.LookupASNForIPs` once, and set the fields. Log and continue if the lookup fails. Do not enrich DNS events.

- [ ] **Step 3: Build and test**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/coordinator/domain/repository.go internal/coordinator/service/events.go
git commit -m "feat: enrich live traffic events with ASN"
```

---

### Task 4: Frontend traffic types, API and shared country flag

**Files:**
- Modify: `web/src/lib/api.ts`
- Modify: `web/src/lib/utils.ts`
- Modify: `web/src/pages/AsnsPage.vue`

**Interfaces:**
- Produces: `TrafficEvent` and `api.traffic(params)`.
- Produces: `countryFlag(code: string): string` in `lib/utils.ts`.
- `Flow`/`BlockEvent` gain optional `dstAsn`/`dstCountry`.

- [ ] **Step 1: Add types and the API method**

```ts
export interface TrafficEvent {
  kind: 'connection' | 'block'
  eventId: string
  nodeId: string
  at: string
  endedAt?: string
  durationMs?: number
  srcIp: string
  srcPort: number
  dstIp: string
  dstPort: number
  protocol: number
  domain?: string
  sni?: string
  target?: string
  blockKind?: string
  containerId?: string
  containerName?: string
  bytesIn?: number
  bytesOut?: number
  packetsIn?: number
  packetsOut?: number
  blocked: boolean
  ruleId?: string
  reason?: string
  dstAsn?: number
  dstCountry?: string
}

traffic: (params: Params) => request<Page<TrafficEvent>>(`/events/traffic${query(params)}`),
```

Add `dstAsn?`/`dstCountry?` to `Flow` and `BlockEvent`.

- [ ] **Step 2: Move `countryFlag` to utils**

Add `countryFlag` to `lib/utils.ts`; in `AsnsPage.vue` import it and remove the local definition. Keep the flag CSS class.

- [ ] **Step 3: Typecheck**

Run: `cd web && bun run typecheck`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add web/src/lib/api.ts web/src/lib/utils.ts web/src/pages/AsnsPage.vue
git commit -m "feat: add frontend traffic event types"
```

---

### Task 5: Traffic and DNS tabs with the new table

**Files:**
- Modify: `web/src/pages/EventsPage.vue`
- Modify: `web/src/styles/app.css`

**Interfaces:**
- Consumes: `api.traffic`, `TrafficEvent`, `countryFlag`, `StatusMark`.
- Produces: `Traffic` tab (default) and `DNS` tab; Blocks tab removed.

- [ ] **Step 1: Replace the tab state**

`type Tab = 'traffic' | 'dns'`. Remove `blocks` page state and the `/events/blocks` load path. Keep DNS state and live DNS. Rename labels: `Traffic`, `DNS`.

- [ ] **Step 2: Load traffic**

In `load()`, call:

```ts
traffic.value = await api.traffic({
  ...common,
  domain: search.value || undefined,
  ip: ip.value || undefined,
  decision: filter.value === 'all' ? undefined : filter.value,
})
```

For DNS keep the existing call. `total`/`page` switch on `traffic`/`dns`.

- [ ] **Step 3: Render the Traffic table**

Columns: **Time**, **Node**, **Source**, **Destination**, **Target / domain**, **ASN**, **Country**, **Egress**, **Decision**.

- Source: `${srcIp}${srcPort ? `:${srcPort}` : ''}`; Destination likewise.
- Target: `target || sni || domain || '—'`; prefix a `<span class="tag">{{ blockKind }}</span>` for block rows.
- ASN: `AS${dstAsn}` or `—`.
- Country: flag + code or `—`.
- Decision: `StatusMark` blocked/allowed.
- Row class: `:class="{ 'pc-row-blocked': event.blocked }"`.
- Empty/loading colspans = 9.

- [ ] **Step 4: Detail dialog and filters**

Replace `showFlow`/`showBlock` with `showTraffic(event)` that lists Node, time, kind, source, destination, target, block kind, protocol, domain/SNI, container, bytes, packets, duration (connections), ASN/country, rule, reason, event ID.

Filter options for Traffic: any decision, allowed, blocked. Search placeholder: `Domain / target`. Keep node/container/IP/range/live controls.

- [ ] **Step 5: Blocked row styling**

```css
.pc-table tr.pc-row-blocked td { background: var(--iris-bg); }
.pc-table tr.pc-row-blocked td:first-child { box-shadow: inset 2px 0 0 var(--iris); }
```

Apply the class to blocked DNS rows too.

- [ ] **Step 6: Typecheck and build**

Run: `cd web && bun run typecheck && bun run build`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add web/src/pages/EventsPage.vue web/src/styles/app.css
git commit -m "feat: add unified traffic events table"
```

---

### Task 6: Live traffic rows

**Files:**
- Modify: `web/src/pages/EventsPage.vue`

**Interfaces:**
- Consumes: `liveFlows`, `liveBlocks`, `liveDns`.
- Produces: live prepend on the Traffic tab when offset 0 and no filters; blocked live flows are ignored.

- [ ] **Step 1: Map live events**

Add helpers:

```ts
function flowToTraffic(flow: Flow): TrafficEvent
function blockToTraffic(block: BlockEvent): TrafficEvent
```

Connection `kind: 'connection'`; block `kind: 'block'`, `blocked: true`, `target`, `blockKind`, `dstAsn`/`dstCountry` if present.

- [ ] **Step 2: Prepend merged live rows**

Watch `liveFlows` and `liveBlocks`; when `tab === 'traffic'`, `offset === 0`, `noFilters`, and `traffic.value`, filter `liveFlows` to `!flow.blocked`, map both, sort by `at` descending, drop IDs already present, prepend, and cap at `limit`.

Keep the existing DNS live watcher.

- [ ] **Step 3: Verify**

Run: `cd web && bun run typecheck && bun run build`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add web/src/pages/EventsPage.vue
git commit -m "feat: stream live rows into traffic table"
```

---

### Task 7: Bigger brand mark using the new icon

**Files:**
- Create: `web/public/logo-128.png`
- Modify: `web/index.html`
- Modify: `web/src/App.vue`
- Modify: `web/src/pages/LoginPage.vue`
- Modify: `web/src/styles/app.css`
- Modify: `internal/coordinator/httpapi/router.go`

**Interfaces:**
- Produces: `/logo-128.png` served by the coordinator.
- Header brand mark displays at 28×28; login brand mark at 40×40.

- [ ] **Step 1: Generate the asset**

```bash
magick fav.png -resize 128x128 -strip web/public/logo-128.png
```

Add `logo-128.png` to the static-file whitelist. No new `<link>` is needed; the favicon links stay.

- [ ] **Step 2: Replace the CSS square**

Header:

```html
<img class="logo-mark" src="/logo-128.png" alt="" width="28" height="28" />
```

Login:

```html
<img class="logo-mark auth-logo" src="/logo-128.png" alt="" width="40" height="40" />
```

CSS:

```css
.logo-mark { width: 28px; height: 28px; flex: none; display: block; object-fit: contain; }
.auth-logo { width: 40px; height: 40px; }
```

Remove `.logo-mark::before`.

- [ ] **Step 3: Verify**

Run: `cd web && bun run typecheck && bun run build`
Expected: PASS; `/logo-128.png` is emitted into `dist/`.

- [ ] **Step 4: Commit**

```bash
git add web/public/logo-128.png web/index.html web/src/App.vue web/src/pages/LoginPage.vue web/src/styles/app.css internal/coordinator/httpapi/router.go
git commit -m "feat: enlarge brand mark with project icon"
```

---

### Task 8: Deploy and integration verification

**Files:**
- None (verification only; fix and amend if a check fails).

- [ ] **Step 1: Rebuild and restart**

```bash
cd deploy
docker compose -f compose.dev.yml build coordinator
docker compose -f compose.dev.yml up -d --force-recreate coordinator
```

- [ ] **Step 2: Wait for ASN ranges**

```bash
docker compose -f compose.dev.yml logs --tail 50 coordinator | grep -i "ASN catalog"
```

Expected: a log line with `asns` and `ranges`; the ranges count is ~579k. If the directory is fresh but ranges are empty, the forced reload must run.

- [ ] **Step 3: Verify the API**

Log in and call `/api/v1/events/traffic?limit=5`. Confirm:
- both `kind: "connection"` and `kind: "block"` rows;
- no blocked flow duplicates;
- `dstAsn`/`dstCountry` for `8.8.8.8` and `2001:4860:4860::8888`;
- allowed/blocked filters and totals.

- [ ] **Step 4: Verify the UI in Chromium**

- Traffic is the default tab; DNS remains.
- Source/Destination are separate columns.
- Blocked rows are tinted and show the block kind.
- ASN/country columns populate.
- Detail dialog shows all fields.
- Live prepend works while the tab is active.
- Header and login brand marks are visibly larger and use the new icon.
- Mobile layout has no horizontal page overflow (table may scroll).

- [ ] **Step 5: Run all checks**

```bash
go test ./... && go vet ./... && gofmt -l cmd internal gen migrations
cd web && bun run typecheck && bun run build
```

- [ ] **Step 6: Commit fixes and push**

```bash
git add -A
git commit -m "chore: verify unified traffic events"
git push origin main
```