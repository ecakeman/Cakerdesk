CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE threads (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  title TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE messages (
  id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  goal TEXT NOT NULL,
  status TEXT NOT NULL,
  plan_snapshot TEXT NOT NULL DEFAULT '',
  verification_snapshot TEXT NOT NULL DEFAULT '',
  artifact_paths TEXT NOT NULL DEFAULT '[]',
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE events (
  run_id TEXT NOT NULL,
  seq BIGINT NOT NULL,
  type TEXT NOT NULL,
  timestamp TEXT NOT NULL,
  payload TEXT NOT NULL,
  PRIMARY KEY (run_id, seq)
);
