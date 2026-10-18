-- The queue as a caller reads it, through internal/repository/postgres.

-- Returns the id rather than reporting a row count: Postgres has no
-- last-insert-id, and RETURNING is how a caller learns what it queued.
-- name: EnqueueJob :one
INSERT INTO ingest_jobs (user_id, source_path, title, force, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'queued', now(), now())
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

-- A queued job is cancelled on the spot, since no worker holds it to notice
-- a request; a running one is asked, and its worker stops at the next
-- checkpoint. A job already finished is left as it ended.
-- name: RequestJobCancel :exec
UPDATE ingest_jobs
   SET status = CASE WHEN status = 'queued' THEN 'cancelled' ELSE status END,
       error = CASE WHEN status = 'queued' THEN 'cancelled at operator request' ELSE error END,
       cancel_req = (status = 'running'),
       updated_at = now()
 WHERE id = $1 AND status IN ('queued', 'running');
