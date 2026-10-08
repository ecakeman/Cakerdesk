-- +goose Up
CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE threads (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects (id),
  title TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE messages (
  id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL REFERENCES threads (id),
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects (id),
  thread_id TEXT NOT NULL REFERENCES threads (id),
  goal TEXT NOT NULL,
  status TEXT NOT NULL,
  plan_snapshot TEXT NOT NULL DEFAULT '',
  verification_snapshot TEXT NOT NULL DEFAULT '',
  artifact_paths TEXT NOT NULL DEFAULT '[]',
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE events (
  run_id TEXT NOT NULL REFERENCES runs (id),
  seq BIGINT NOT NULL,
  type TEXT NOT NULL,
  timestamp TEXT NOT NULL,
  payload TEXT NOT NULL,
  PRIMARY KEY (run_id, seq)
);

-- +goose Down
DROP TABLE events;
DROP TABLE runs;
DROP TABLE messages;
DROP TABLE threads;
DROP TABLE projects;
