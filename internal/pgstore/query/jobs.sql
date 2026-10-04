-- The queue as a caller reads it, through internal/repository/postgres.

-- Returns the id rather than reporting a row count: Postgres has no
-- last-insert-id, and RETURNING is how a caller learns what it queued.
-- name: EnqueueJob :one
INSERT INTO ingest_jobs (user_id, source_path, title, status, created_at, updated_at)
VALUES ($1, $2, $3, 'queued', now(), now())
RETURNING id;

-- Position counts the job itself, so a freshly queued job with nothing ahead
-- of it reports 1 rather than 0.
-- name: QueuePosition :one
SELECT COUNT(*) FROM ingest_jobs
 WHERE status IN ('queued','running') AND id <= $1;

-- The three job reads select * so they share one generated row type and one
-- mapper. Column-order drift, the usual reason to avoid *, is what sqlc
-- removes: the struct and the scan are generated from the schema together.
-- name: JobByID :one
SELECT * FROM ingest_jobs WHERE id = $1;

-- name: ActiveJobs :many
SELECT * FROM ingest_jobs
 WHERE status IN ('queued','running')
 ORDER BY created_at, id;

-- name: RecentJobs :many
SELECT * FROM ingest_jobs
 ORDER BY (status IN ('queued','running')) DESC, updated_at DESC
 LIMIT $1;

-- name: JobStatus :one
SELECT status FROM ingest_jobs WHERE id = $1;

-- name: RequestJobCancel :exec
UPDATE ingest_jobs SET cancel_req = true, updated_at = now() WHERE id = $1;
