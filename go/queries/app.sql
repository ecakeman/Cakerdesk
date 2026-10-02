-- name: CreateAgent :one
INSERT INTO agents (name)
VALUES ($1)
RETURNING id, name, current_version, created_at, updated_at;

-- name: GetAgent :one
SELECT id, name, current_version, created_at, updated_at
FROM agents
WHERE id = $1;

-- name: ListAgents :many
SELECT id, name, current_version, created_at, updated_at
FROM agents
ORDER BY created_at DESC
LIMIT 200;

-- name: LockAgent :one
SELECT id, name, current_version, created_at, updated_at
FROM agents
WHERE id = $1
FOR UPDATE;

-- name: GetAgentVersion :one
SELECT agent_id, version, config, config_hash, created_at
FROM agent_versions
WHERE agent_id = $1 AND version = $2;

-- name: GetCurrentAgentVersion :one
SELECT v.agent_id, v.version, v.config, v.config_hash, v.created_at
FROM agent_versions v
JOIN agents a ON a.id = v.agent_id AND a.current_version = v.version
WHERE a.id = $1;

-- name: MaxAgentVersion :one
SELECT COALESCE(MAX(version), 0)::int AS max_version
FROM agent_versions
WHERE agent_id = $1;

-- name: InsertAgentVersion :one
INSERT INTO agent_versions (agent_id, version, config, config_hash)
VALUES ($1, $2, $3, $4)
RETURNING agent_id, version, config, config_hash, created_at;

-- name: SetAgentCurrentVersion :exec
UPDATE agents
SET current_version = $2, updated_at = now()
WHERE id = $1;

-- name: InsertSession :one
INSERT INTO sessions (agent_id, title)
VALUES ($1, $2)
RETURNING id, agent_id, title, created_at;

-- name: GetSession :one
SELECT id, agent_id, title, created_at
FROM sessions
WHERE id = $1;

-- name: ActiveRunID :one
SELECT id
FROM runs
WHERE session_id = $1 AND status IN ('queued', 'running');

-- name: InsertRun :one
INSERT INTO runs (session_id, agent_id, agent_version, config, input, max_attempts)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, session_id, agent_id, agent_version, config, input, status, attempt,
    max_attempts, result, error_code, error_message, created_at, started_at, finished_at;

-- name: GetRun :one
SELECT id, session_id, agent_id, agent_version, config, input, status, attempt,
    max_attempts, result, error_code, error_message, created_at, started_at, finished_at
FROM runs
WHERE id = $1;

-- name: ListRunsBySession :many
SELECT id, session_id, agent_id, agent_version, config, input, status, attempt,
    max_attempts, result, error_code, error_message, created_at, started_at, finished_at
FROM runs
WHERE session_id = $1
ORDER BY created_at DESC
LIMIT 200;
