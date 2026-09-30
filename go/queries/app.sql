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
