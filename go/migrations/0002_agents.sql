-- +goose Up
CREATE TABLE agents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    current_version int,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agents_name_format CHECK (name ~ '^[a-z0-9][a-z0-9-]{1,62}$')
);

CREATE TABLE agent_versions (
    agent_id uuid NOT NULL REFERENCES agents (id),
    version int NOT NULL CHECK (version >= 1),
    config jsonb NOT NULL,
    config_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, version)
);

REVOKE UPDATE, DELETE ON agent_versions FROM cd_app;

-- +goose Down
DROP TABLE IF EXISTS agent_versions;
DROP TABLE IF EXISTS agents;
