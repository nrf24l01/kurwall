-- +goose Up
-- Deliberately no primary key or unique constraint: a multi-origin prefix can
-- produce several ranges sharing the same start_ip. Lookups use
-- start_ip <= ip ORDER BY start_ip DESC LIMIT 1, and the whole table is
-- replaced atomically on catalog refresh.
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
