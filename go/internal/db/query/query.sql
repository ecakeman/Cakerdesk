-- name: CreateProject :exec
INSERT INTO projects (id, name, created_at) VALUES ($1, $2, $3);

-- name: ListProjects :many
SELECT id, name FROM projects ORDER BY created_at;

-- name: CreateThread :exec
INSERT INTO threads (id, project_id, title, created_at) VALUES ($1, $2, $3, $4);

-- name: ListThreads :many
SELECT id, title FROM threads WHERE project_id = $1 ORDER BY created_at;

-- name: GetThread :one
SELECT id, project_id, title FROM threads WHERE id = $1;

-- name: AddMessage :exec
INSERT INTO messages (id, thread_id, role, content, created_at) VALUES ($1, $2, $3, $4, $5);

-- name: ListMessages :many
SELECT role, content FROM messages WHERE thread_id = $1 ORDER BY created_at;

-- name: CreateRun :exec
INSERT INTO runs (id, project_id, thread_id, goal, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetRun :one
SELECT id, project_id, thread_id, goal, status, plan_snapshot, verification_snapshot, artifact_paths
FROM runs WHERE id = $1;

-- name: SetRunStatus :exec
UPDATE runs SET status = $2 WHERE id = $1;

-- name: RequestCancel :execrows
UPDATE runs SET status = 'cancel_requested' WHERE id = $1 AND status = 'running';

-- name: UpdatePlanSnapshot :exec
UPDATE runs SET plan_snapshot = $2 WHERE id = $1;

-- name: UpdateVerificationSnapshot :exec
UPDATE runs SET verification_snapshot = $2 WHERE id = $1;

-- name: CompleteRun :exec
UPDATE runs SET status = 'completed', artifact_paths = $2 WHERE id = $1;

-- name: InsertEvent :execrows
INSERT INTO events (run_id, seq, type, timestamp, payload)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (run_id, seq) DO NOTHING;

-- name: ListEventsAfter :many
SELECT run_id, seq, type, timestamp, payload
FROM events
WHERE run_id = $1 AND seq > $2
ORDER BY seq;
