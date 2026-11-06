-- Version 19: a job records who queued it, and the worker writes as them.
--
-- A job used to belong to a user, who was also its library. In a shared
-- library the two differ, and the worker needs a user to write as: the
-- policies check a grant on every statement, so a worker writing as the
-- person who queued the job is checked against that person's grant. Someone
-- removed as an editor while their job waited cannot have it written for
-- them.
--
-- The worker finds work before it knows whom to write as, and the request
-- role sees only the libraries its user holds grants on, so finding work
-- goes through two definer functions. waiting_queues names each library and
-- requester with a job to claim or a lease to recover, and only where the
-- requester can still add to the library. fail_unclaimable_jobs fails the
-- queued jobs no one can be written as any longer, which would otherwise
-- wait forever. Both return or change job states and identifiers, never a
-- document.
--
-- +goose Up
-- +goose StatementBegin
ALTER TABLE ingest_jobs ADD COLUMN requested_by text REFERENCES users(user_id) ON DELETE SET NULL;

-- Every job so far was queued in a personal library by its owner. FORCE binds
-- the role running this, so a policy of its own lets it see the rows.
CREATE POLICY migrate ON ingest_jobs TO CURRENT_USER USING (true) WITH CHECK (true);
UPDATE ingest_jobs SET requested_by = library_id
 WHERE library_id IN (SELECT user_id FROM users);
DROP POLICY migrate ON ingest_jobs;

CREATE INDEX ingest_jobs_waiting ON ingest_jobs(library_id, requested_by)
  WHERE status IN ('queued', 'running');

-- The two functions below run as the role that owns them and read every
-- library's queue. Requests never run as that role.
CREATE POLICY worker ON ingest_jobs TO CURRENT_USER USING (true) WITH CHECK (true);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION waiting_queues()
RETURNS TABLE (library_id text, requested_by text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT DISTINCT j.library_id, j.requested_by
    FROM ingest_jobs j
    JOIN library_grants g ON g.library_id = j.library_id AND g.user_id = j.requested_by
   WHERE role_rank(g.role) >= role_rank('editor')
     AND (j.status = 'queued' OR (j.status = 'running' AND j.lease_until < now()))
   ORDER BY j.library_id, j.requested_by;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION fail_unclaimable_jobs()
RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  failed integer;
BEGIN
  UPDATE ingest_jobs j
     SET status = 'failed', permanent = true, lease_until = NULL, updated_at = now(),
         error = 'the account that queued it can no longer add to this library'
   WHERE j.status = 'queued'
     AND NOT EXISTS (
       SELECT 1 FROM library_grants g
        WHERE g.library_id = j.library_id AND g.user_id = j.requested_by
          AND role_rank(g.role) >= role_rank('editor'));
  GET DIAGNOSTICS failed = ROW_COUNT;
  RETURN failed;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
REVOKE ALL ON FUNCTION waiting_queues() FROM PUBLIC;
REVOKE ALL ON FUNCTION fail_unclaimable_jobs() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION waiting_queues() TO docsearch_app;
GRANT EXECUTE ON FUNCTION fail_unclaimable_jobs() TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION fail_unclaimable_jobs();
DROP FUNCTION waiting_queues();
DROP POLICY worker ON ingest_jobs;
DROP INDEX ingest_jobs_waiting;
ALTER TABLE ingest_jobs DROP COLUMN requested_by;
-- +goose StatementEnd
