# Unified traffic events with destination ASN and country

Date: 2026-11-12
Status: proposed

## Intent

The Events page currently splits traffic into **Connections**, **DNS** and
**Blocks**. Connections combines source and destination into one cell, and
blocking decisions live in a separate tab. The user wants one traffic view that
shows allowed and blocked events together, source and destination in separate
columns, blocked rows visibly coloured, and the destination's ASN and country.

## Goals

- Replace the Connections and Blocks tabs with one **Traffic** table.
- Keep **DNS** as a separate tab for query-level detail.
- Show every allowed connection and every blocking decision in one
  chronological list.
- Split **Source** and **Destination** into separate columns (`ip:port`).
- Resolve the destination IP to its ASN and ASN country.
- Colour blocked rows with the existing blocked/iris state colour.
- Keep live updates working for the Traffic table.
- Preserve the detail dialog with the full event fields.

## Non-goals

- IP geolocation (city, region, coordinates). Country comes from the ASN
  registry data.
- Changing retention or ClickHouse TTLs.
- Filtering/grouping by ASN or country in this iteration (display only).
- Changing the agent event schema or protobuf.

## Anti-goals

- Showing the same block twice (once as a blocked flow and once as a block
  event).
- Coupling destination ASN display to ASN rule resolution being enabled.
- Making the coordinator do an external lookup per event.
- Slowing ingestion down when ASN data is unavailable.

## Approaches considered

1. **Unified traffic query (chosen).** A new ClickHouse `UNION ALL` over
   allowed flows and block events, plus read-time ASN enrichment through
   PostgreSQL. One API endpoint, one table.
2. Merge flows and blocks in Go after separate queries. Rejected: pagination
   and total counts become incorrect or expensive.
3. Store ASN/country on every ClickHouse row at ingestion. This enables fast
   filters and grouping but requires schema changes and a backfill for
   historical events. Deferred; display only does not need it.

For ASN data, reusing the iptoasn prefix ranges already downloaded for the ASN
catalog avoids an external API. The combined feed contains both IPv4 and IPv6
ranges, so both families are kept. MaxMind GeoLite2 would add city-level data
but requires a license and a new data pipeline.

## UX

### Tabs

- **Traffic** (default)
- **DNS**

The Blocks tab is removed. The existing `/events/blocks` API remains for
compatibility, but the dashboard no longer uses it.

### Traffic table

```
Time            Node   Source              Destination         Target / domain   ASN       Country   Decision
11/12 14:22:03  edge-1 10.0.13.62:51234    142.250.74.78:443   google.com        AS15169   🇺🇸 US     allowed
11/12 14:22:01  edge-1 10.0.13.62:51233    8.8.8.8:53          example.com       AS15169   🇺🇸 US     blocked (dns)
11/12 14:21:58  edge-1 10.0.13.62:51232    185.199.108.153:443 —                 AS54113   🇺🇸 US     allowed
```

- Blocked rows use a subtle iris background (`--iris-bg`), an inset iris
  left border, and the existing blocked `StatusMark`.
- A block row also shows its rule kind (`dns`, `domain`, `ip`, `cidr`, `asn`)
  next to the decision.
- Connection rows show the domain/SNI. Block rows show the rule target.
- ASN and country are separate columns; country includes the flag.
- The detail dialog adds destination ASN, destination country, and block
  kind/target.

### Filters

- Node.
- Text search: domain/SNI for connections, target/domain for blocks.
- Container.
- IP: matches either source or destination (the API also keeps separate
  `srcIp`/`dstIp` parameters).
- Decision: any / allowed / blocked.
- Range: 1 h / 24 h / 7 d, as today.
- Live chip, as today.

## Backend

### New API

`GET /api/v1/events/traffic`

Query parameters: `nodeId`, `from`, `to`, `limit`, `offset`, `decision`
(`allowed`|`blocked`), `domain`, `container`, `ip`, `srcIp`, `dstIp`,
`protocol`.

Response:

```json
{
  "items": [
    {
      "kind": "connection",
      "eventId": "…",
      "nodeId": "test-node-1",
      "at": "2026-11-12T14:22:03Z",
      "endedAt": "2026-11-12T14:22:04Z",
      "durationMs": 1000,
      "srcIp": "10.0.13.62", "srcPort": 51234,
      "dstIp": "142.250.74.78", "dstPort": 443,
      "protocol": 6,
      "domain": "google.com", "sni": "google.com",
      "containerName": "",
      "bytesIn": 0, "bytesOut": 0,
      "packetsIn": 0, "packetsOut": 0,
      "blocked": false, "reason": "allowed", "ruleId": "",
      "dstAsn": 15169, "dstCountry": "US"
    },
    {
      "kind": "block",
      "eventId": "…",
      "nodeId": "test-node-1",
      "at": "2026-11-12T14:22:01Z",
      "srcIp": "10.0.13.62", "srcPort": 51233,
      "dstIp": "8.8.8.8", "dstPort": 53,
      "protocol": 17,
      "domain": "", "target": "example.com", "blockKind": "dns",
      "blocked": true, "reason": "…", "ruleId": "…",
      "dstAsn": 15169, "dstCountry": "US"
    }
  ],
  "total": 2, "limit": 50, "offset": 0
}
```

### ClickHouse query

The Traffic reader performs a `UNION ALL` inside a single ClickHouse query:

- Connections: `blocked = 0` rows from `flows` (`at = started_at`, with
  bytes/packets/duration).
- Blocks: all rows from `block_events` (`at = at`, `blocked = 1`,
  `target`, `blockKind = kind`).
- Blocked rows from `flows` are **excluded**. Every block path already emits a
  `block_events` row (IP, CIDR, domain, ASN, DNS); including `blocked = 1`
  flows would duplicate them. The block event is the authoritative decision.
- **Block granularity:** one row per blocking decision. IP/CIDR/ASN kernel
  drops are deduplicated by the agent within a 10-second window, while domain
  and DNS blocks are recorded per attempt. The Traffic total is therefore a
  decision count, not a connection-attempt count, and is not expected to equal
  `/stats/overview`'s `blockedFlows`. This preserves the behavior of the
  existing Blocks tab.
- Filters are applied inside each branch before the union, so the existing
  `ORDER BY` keys and time partitions are used.
- The outer query orders by `at DESC, eventId DESC` and applies limit/offset.
- The total is `count()` over the same union.

`EventReader` gains `Traffic(ctx, TrafficQuery) ([]TrafficEvent, uint64, error)`.
`TrafficQuery` carries the filters above.

### ASN and country

The existing catalog loader parses iptoasn's combined TSV but currently
deduplicates by ASN and discards the ranges. Change it to return both:

- `asn_catalog` (search directory), as today.
- New `asn_ranges` table:

```sql
CREATE TABLE asn_ranges (
    start_ip inet NOT NULL,
    end_ip   inet NOT NULL,
    asn      bigint NOT NULL,
    country  text NOT NULL DEFAULT '',
    name     text NOT NULL DEFAULT ''
);
CREATE INDEX asn_ranges_start_idx ON asn_ranges (start_ip);
```

`ReplaceASNCatalog` truncates and copies both tables in one transaction. If
`asn_ranges` is empty while the catalog is fresh, the background catalog task
forces a reload (this also covers the first start after deployment).

`LookupASNForIPs(ctx, ips []string) map[string]ASNInfo` in PostgreSQL uses one
`unnest` + `LATERAL` query. The `inet` columns and comparison handles both
IPv4 and IPv6:

```sql
SELECT ip, r.asn, r.country, r.name
FROM unnest($1::inet[]) AS ip
LEFT JOIN LATERAL (
    SELECT asn, country, name, end_ip
    FROM asn_ranges
    WHERE start_ip <= ip
    ORDER BY start_ip DESC
    LIMIT 1
) r ON r.end_ip >= ip
```

This is one indexed lookup per IP inside a single query. Unmatched addresses
(private space, gaps in the feed) return no ASN and the UI shows `—`.

The DNS-block case uses the resolver IP as the destination, so its ASN/country
is the resolver's; the target column still shows the blocked domain. This is
documented in the detail dialog.

### ASN feature coupling

The ASN catalog and range data are loaded whenever `asn.catalog_enabled` is
true, regardless of `asn.enabled`. The resolver/rule feature stays gated by
`asn.enabled`. This makes the ASN directory page and Traffic ASN display work
for operators who do not use ASN blocking rules.

### Enrichment

- **History:** the Events service enriches `Traffic` rows after the ClickHouse
  read, collecting unique destination IPs and performing one lookup.
- **Live:** `Events.Ingest` enriches flow/block records after the insert and
  before `PublishEvents`, using the same batched lookup. The live JSON carries
  `dstAsn`/`dstCountry`. Enrichment failure is logged and does not fail
  ingestion; events still publish without ASN data.
- No ClickHouse schema change and no backfill; historical events are enriched
  at read time.

### Live dashboard

- The Traffic tab prepends new live rows when the tab is active, offset is 0,
  and no filters are set.
- Live flows with `blocked = true` are ignored (their block event is already in
  `liveBlocks`).
- The DNS tab keeps its existing live prepend behavior.

## Frontend changes

- `EventsPage.vue`: two tabs, new Traffic table, row tint, separate source and
  destination columns, ASN/country columns, decision filter, detail fields.
- `api.ts`: `TrafficEvent`, `traffic(params)`.
- `Flow`/`BlockEvent` gain optional `dstAsn`/`dstCountry` for live frames.
- `utils.ts`: shared `countryFlag` (move from `AsnsPage.vue`).
- CSS: `.pc-row-blocked` tint for the Traffic and DNS tables.
- The Blocks tab and its `blocks` page state are removed.

## Constraints and known limitations

- Country is the ASN holder's registered country, not the physical server
  location.
- ClickHouse unions and counts are bounded by the 30-day TTL and the selected
  time window; the existing 7-day UI maximum is unchanged.
- The first catalog refresh after deploy downloads the existing 128 MB iptoasn
  file and copies roughly 579k IPv4+IPv6 ranges; ASN columns show `—` until it
  completes.
- The old `/events/flows`, `/events/dns` and `/events/blocks` endpoints remain
  for API compatibility.

## Verification

- Unit test the catalog parser for range extraction and the SQL lookup helper
  where practical.
- With the running coordinator and test node: Traffic shows allowed flows and
  blocked rows; source/destination are separate; blocked rows are tinted; ASN
  and country appear for known IPv4 and IPv6 destinations (e.g. `8.8.8.8` and
  `2001:4860:4860::8888` → AS15169/US); blocked flows are not duplicated;
  pagination totals are consistent with allowed flows + block decisions; live
  prepend works; DNS tab is unchanged.
- `go test ./...`, `go vet`, `gofmt`, `bun run typecheck`, `bun run build`.

## Decisions left to the builder

- Exact ClickHouse column aliases and scan helpers.
- The detail-dialog field order and column widths.
- The precise copy for the "no ASN data" placeholder.