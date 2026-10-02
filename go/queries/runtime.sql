-- 领取必须是一条语句。先查再更新时，两个 worker 会领到同一行。
-- SKIP LOCKED 让后到的语句跳过已经被锁住的行，而不是排队等它提交。
-- name: ClaimRun :one
WITH picked AS (
    SELECT id
    FROM runs
    WHERE status = 'queued'
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE runs
SET status = 'running',
    attempt = attempt + 1,
    lease_owner = sqlc.arg('lease_owner'),
    lease_expires_at = now() + make_interval(secs => sqlc.arg('lease_seconds')::int),
    started_at = COALESCE(started_at, now()),
    updated_at = now()
FROM picked
WHERE runs.id = picked.id
RETURNING runs.id, runs.session_id, runs.attempt, runs.config, runs.input;

-- name: CompleteRun :execrows
UPDATE runs
SET status = sqlc.arg('status'),
    result = sqlc.arg('result'),
    error_code = sqlc.arg('error_code'),
    error_message = sqlc.arg('error_message'),
    finished_at = now(),
    lease_owner = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND attempt = sqlc.arg('attempt')
  AND lease_owner = sqlc.arg('lease_owner')
  AND status = 'running';
