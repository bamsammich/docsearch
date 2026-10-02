-- The worker's job statements, through internal/repository/postgres.
--
-- Crash recovery rests on the lease. A claimed job carries lease_until, and
-- the claim takes back any running job whose lease has expired, so a worker
-- killed mid-job is recovered by the next one without intervention.

-- Claims one job.
--
-- The deadline is computed by the database rather than the worker, so every
-- worker agrees on when a lease expired whatever its own clock says.
--
-- FOR UPDATE SKIP LOCKED is what lets several workers claim at once: each
-- one locks the row it picked and passes over rows another worker has
-- already locked, so two workers racing take two different jobs rather than
-- contending for one. SQLite allowed a single writer, so the same statement
-- there relied on the write lock instead.
--
-- Jobs at or past the attempt ceiling are excluded here rather than after
-- claiming: a permanently failing job that is claimed and then put back
-- would be reclaimed forever and starve the queue.
-- name: ClaimJob :one
UPDATE ingest_jobs
   SET status = 'running',
       attempts = attempts + 1,
       phase = NULL,
       progress_cur = NULL,
       progress_tot = NULL,
       error = NULL,
       lease_until = now() + make_interval(secs => sqlc.arg(lease_seconds)),
       updated_at = now()
 WHERE id = (
       SELECT next.id FROM ingest_jobs next
        WHERE next.cancel_req = false
          AND next.attempts < sqlc.arg(max_attempts)
          AND (next.status = 'queued'
               OR (next.status = 'running' AND next.lease_until < now()))
        ORDER BY next.created_at, next.id
        LIMIT 1
        FOR UPDATE SKIP LOCKED)
RETURNING *;

-- Every progress write renews the lease: a job that is visibly advancing must
-- never be reclaimed out from under the worker running it.
-- name: RecordJobProgress :exec
UPDATE ingest_jobs
   SET phase = $1, progress_cur = $2, progress_tot = $3,
       lease_until = now() + make_interval(secs => sqlc.arg(lease_seconds)), updated_at = now()
 WHERE id = sqlc.arg(id);

-- Recorded while the ingest runs, so a job that dies mid-write still says
-- which document it was writing.
-- name: RecordJobDocID :exec
UPDATE ingest_jobs
   SET doc_id = $1, updated_at = now()
 WHERE id = $2;

-- name: JobCancelRequested :one
SELECT cancel_req FROM ingest_jobs WHERE id = $1;

-- name: JobDocID :one
SELECT doc_id FROM ingest_jobs WHERE id = $1;

-- name: CancelJob :exec
UPDATE ingest_jobs
   SET status = 'cancelled', phase = NULL, lease_until = NULL,
       error = 'cancelled at operator request', updated_at = now()
 WHERE id = $1;

-- attempts is left at its true value. A status of 'failed' is already
-- unclaimable, since the claim considers only 'queued' and lease-expired
-- 'running', so inflating the counter to block a reclaim would buy nothing
-- and would make a job that failed deterministically on its first attempt
-- indistinguishable from one that genuinely exhausted three. The permanent
-- flag carries that distinction instead.
-- name: FinishJob :exec
UPDATE ingest_jobs
   SET status = $1, phase = NULL, error = $2, permanent = $3,
       lease_until = NULL, updated_at = now()
 WHERE id = $4;

-- Completes a job whose ingest wrote nothing, which is what an unchanged
-- source produces: the transaction that would have completed the job
-- alongside the document never ran, because no document was written.
-- name: CompleteJobWithoutDocument :exec
UPDATE ingest_jobs
   SET status = 'done', phase = NULL, doc_id = $1, error = NULL,
       lease_until = NULL, updated_at = now()
 WHERE id = $2;
