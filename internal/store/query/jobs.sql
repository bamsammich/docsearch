-- name: EnqueueJob :execresult
INSERT INTO ingest_jobs (source_path, title, status, created_at, updated_at)
VALUES (?, ?, 'queued', datetime('now'), datetime('now'));

-- Position counts the job itself, so a freshly queued job with nothing ahead
-- of it reports 1 rather than 0.
-- name: QueuePosition :one
SELECT COUNT(*) FROM ingest_jobs
 WHERE status IN ('queued','running') AND id <= ?;

-- name: JobByID :one
SELECT id, source_path, title, doc_id, status, phase, progress_cur,
       progress_tot, attempts, permanent, error, warnings, lease_until,
       created_at, updated_at
  FROM ingest_jobs WHERE id = ?;

-- name: ActiveJobs :many
SELECT id, source_path, title, doc_id, status, phase, progress_cur,
       progress_tot, attempts, permanent, error, warnings, lease_until,
       created_at, updated_at
  FROM ingest_jobs
 WHERE status IN ('queued','running')
 ORDER BY created_at, id;

-- name: RecentJobs :many
SELECT id, source_path, title, doc_id, status, phase, progress_cur,
       progress_tot, attempts, permanent, error, warnings, lease_until,
       created_at, updated_at
  FROM ingest_jobs
 ORDER BY (status IN ('queued','running')) DESC, updated_at DESC
 LIMIT ?;

-- name: JobStatus :one
SELECT status FROM ingest_jobs WHERE id = ?;

-- name: RequestJobCancel :exec
UPDATE ingest_jobs SET cancel_req = 1, updated_at = datetime('now') WHERE id = ?;
