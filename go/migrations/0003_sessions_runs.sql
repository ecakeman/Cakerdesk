-- +goose Up
CREATE TABLE sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id uuid NOT NULL REFERENCES agents (id),
    title text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id uuid NOT NULL REFERENCES sessions (id),
    agent_id uuid NOT NULL,
    agent_version int NOT NULL,
    config jsonb NOT NULL,
    input text NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    attempt int NOT NULL DEFAULT 0,
    max_attempts int NOT NULL,
    lease_owner text,
    lease_expires_at timestamptz,
    result jsonb,
    error_code text,
    error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (agent_id, agent_version) REFERENCES agent_versions (agent_id, version),
    CONSTRAINT runs_input_len CHECK (char_length(input) BETWEEN 1 AND 20000),
    CONSTRAINT runs_status CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT runs_running_lease CHECK (
        (status = 'running') = (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
    ),
    CONSTRAINT runs_terminal_finished CHECK (
        (status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX runs_one_active_per_session
    ON runs (session_id)
    WHERE status IN ('queued', 'running');

CREATE INDEX runs_queued_fifo
    ON runs (created_at)
    WHERE status = 'queued';

CREATE INDEX runs_running_lease
    ON runs (lease_expires_at)
    WHERE status = 'running';

-- +goose Down
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS sessions;
