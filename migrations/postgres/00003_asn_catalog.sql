-- +goose Up
CREATE TABLE IF NOT EXISTS asn_catalog (
    asn        bigint PRIMARY KEY,
    name       text NOT NULL,
    country    text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS asn_catalog_country_idx ON asn_catalog (country, name);
CREATE INDEX IF NOT EXISTS asn_catalog_name_idx ON asn_catalog (lower(name));

-- +goose Down
DROP TABLE IF EXISTS asn_catalog;
