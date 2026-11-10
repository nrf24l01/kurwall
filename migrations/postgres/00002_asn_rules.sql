-- +goose Up
ALTER TABLE rules DROP CONSTRAINT IF EXISTS rules_kind_check;
ALTER TABLE rules ADD CONSTRAINT rules_kind_check
    CHECK (kind IN ('ip', 'cidr', 'domain', 'asn'));

CREATE TABLE IF NOT EXISTS asn_prefixes (
    asn          bigint PRIMARY KEY,
    holder       text NOT NULL DEFAULT '',
    prefixes     text[] NOT NULL DEFAULT '{}',
    refreshed_at timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz,
    error        text NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE IF EXISTS asn_prefixes;
ALTER TABLE rules DROP CONSTRAINT IF EXISTS rules_kind_check;
ALTER TABLE rules ADD CONSTRAINT rules_kind_check
    CHECK (kind IN ('ip', 'cidr', 'domain'));