-- +goose Up
ALTER TABLE runs
    ADD COLUMN last_seq bigint NOT NULL DEFAULT 0;

CREATE TABLE run_events (
    run_id uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq bigint NOT NULL,
    type text NOT NULL,
    attempt int NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, seq)
);

-- +goose Down
DROP TABLE IF EXISTS run_events;
ALTER TABLE runs DROP COLUMN IF EXISTS last_seq;
