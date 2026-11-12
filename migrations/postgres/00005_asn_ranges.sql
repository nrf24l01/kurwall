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