-- +goose Up
CREATE TABLE IF NOT EXISTS nodes (
    id            text PRIMARY KEY,
    name          text NOT NULL UNIQUE,
    hostname      text NOT NULL DEFAULT '',
    agent_version text NOT NULL DEFAULT '',
    os            text NOT NULL DEFAULT '',
    kernel        text NOT NULL DEFAULT '',
    addresses     text[] NOT NULL DEFAULT '{}',
    status        text NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('online', 'offline', 'pending')),
    policy_version bigint NOT NULL DEFAULT 0,
    last_seen_at  timestamptz,
    labels        jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS policies (
    id          text PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS rules (
    id         text PRIMARY KEY,
    policy_id  text NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('ip', 'cidr', 'domain')),
    value      text NOT NULL,
    action     text NOT NULL DEFAULT 'block' CHECK (action IN ('block')),
    note       text NOT NULL DEFAULT '',
    enabled    boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (policy_id, kind, value)
);

CREATE INDEX IF NOT EXISTS rules_policy_idx ON rules (policy_id);

CREATE TABLE IF NOT EXISTS node_policies (
    node_id   text NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    policy_id text NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    PRIMARY KEY (node_id, policy_id)
);

CREATE INDEX IF NOT EXISTS node_policies_policy_idx ON node_policies (policy_id);

CREATE TABLE IF NOT EXISTS policy_revisions (
    version    bigint PRIMARY KEY,
    actor      text NOT NULL DEFAULT '',
    snapshot   jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ingested_batches (
    node_id      text NOT NULL,
    batch_id     text NOT NULL,
    claimed_at   timestamptz NOT NULL,
    committed_at timestamptz,
    PRIMARY KEY (node_id, batch_id)
);

CREATE INDEX IF NOT EXISTS ingested_batches_cleanup_idx ON ingested_batches (committed_at);

-- +goose Down
DROP TABLE IF EXISTS ingested_batches;
DROP TABLE IF EXISTS policy_revisions;
DROP TABLE IF EXISTS node_policies;
DROP TABLE IF EXISTS rules;
DROP TABLE IF EXISTS policies;
DROP TABLE IF EXISTS nodes;