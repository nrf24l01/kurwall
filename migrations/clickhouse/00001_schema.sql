-- ClickHouse schema for Kurwall. Statements are idempotent and applied at
-- coordinator startup. All tables keep 30 days of data via TTL and use ZSTD
-- compression for compact storage.

CREATE DATABASE IF NOT EXISTS kurwall;

CREATE TABLE IF NOT EXISTS kurwall.flows
(
    node_id        LowCardinality(String) CODEC(ZSTD(3)),
    event_id       String CODEC(ZSTD(3)),
    started_at     DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    ended_at       DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    duration_ms    UInt32 CODEC(T64, ZSTD(1)),
    src_ip         IPv6 CODEC(ZSTD(1)),
    dst_ip         IPv6 CODEC(ZSTD(1)),
    src_port       UInt16 CODEC(T64, ZSTD(1)),
    dst_port       UInt16 CODEC(T64, ZSTD(1)),
    protocol       UInt8 CODEC(T64, ZSTD(1)),
    domain         LowCardinality(String) CODEC(ZSTD(3)),
    sni            LowCardinality(String) CODEC(ZSTD(3)),
    container_id   String CODEC(ZSTD(3)),
    container_name LowCardinality(String) CODEC(ZSTD(3)),
    bytes_in       UInt64 CODEC(T64, ZSTD(1)),
    bytes_out      UInt64 CODEC(T64, ZSTD(1)),
    packets_in     UInt32 CODEC(T64, ZSTD(1)),
    packets_out    UInt32 CODEC(T64, ZSTD(1)),
    blocked        Bool CODEC(ZSTD(1)),
    rule_id        String CODEC(ZSTD(3)),
    reason         LowCardinality(String) CODEC(ZSTD(3)),
    network        LowCardinality(String) CODEC(ZSTD(3)),
    ingested_at    DateTime64(3, 'UTC') DEFAULT now64(3) CODEC(DoubleDelta, ZSTD(1))
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(started_at)
ORDER BY (node_id, started_at, event_id)
TTL toDateTime(started_at) + INTERVAL 30 DAY DELETE
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS kurwall.dns_events
(
    node_id        LowCardinality(String) CODEC(ZSTD(3)),
    event_id       String CODEC(ZSTD(3)),
    at             DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    client_ip      IPv6 CODEC(ZSTD(1)),
    client_port    String CODEC(ZSTD(3)),
    qname          String CODEC(ZSTD(3)),
    qtype          UInt16 CODEC(T64, ZSTD(1)),
    rcode          UInt8 CODEC(T64, ZSTD(1)),
    answers        Array(IPv6) CODEC(ZSTD(3)),
    blocked        Bool CODEC(ZSTD(1)),
    rule_id        String CODEC(ZSTD(3)),
    container_id   String CODEC(ZSTD(3)),
    container_name LowCardinality(String) CODEC(ZSTD(3)),
    latency_ms     UInt32 CODEC(T64, ZSTD(1)),
    ingested_at    DateTime64(3, 'UTC') DEFAULT now64(3) CODEC(DoubleDelta, ZSTD(1))
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(at)
ORDER BY (node_id, at, event_id)
TTL toDateTime(at) + INTERVAL 30 DAY DELETE
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS kurwall.block_events
(
    node_id        LowCardinality(String) CODEC(ZSTD(3)),
    event_id       String CODEC(ZSTD(3)),
    at             DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    kind           LowCardinality(String) CODEC(ZSTD(3)),
    target         String CODEC(ZSTD(3)),
    src_ip         IPv6 CODEC(ZSTD(1)),
    dst_ip         IPv6 CODEC(ZSTD(1)),
    src_port       UInt16 CODEC(T64, ZSTD(1)),
    dst_port       UInt16 CODEC(T64, ZSTD(1)),
    protocol       UInt8 CODEC(T64, ZSTD(1)),
    domain         LowCardinality(String) CODEC(ZSTD(3)),
    container_id   String CODEC(ZSTD(3)),
    container_name LowCardinality(String) CODEC(ZSTD(3)),
    rule_id        String CODEC(ZSTD(3)),
    reason         LowCardinality(String) CODEC(ZSTD(3)),
    ingested_at    DateTime64(3, 'UTC') DEFAULT now64(3) CODEC(DoubleDelta, ZSTD(1))
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(at)
ORDER BY (node_id, at, event_id)
TTL toDateTime(at) + INTERVAL 30 DAY DELETE
SETTINGS index_granularity = 8192;