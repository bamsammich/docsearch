-- Version 27: a job left running by a requester who lost the grant fails.
--
-- fail_unclaimable_jobs failed only queued jobs whose requester could no
-- longer add to the library. A job running when its requester left, or was
-- made a reader, could not be finished by the worker, since row-level
-- security then hid it, and nobody else would claim it. It stayed running
-- for good. It is now failed too, once its lease has run out, so a worker
-- still finishing it is never overtaken.
--
-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION fail_unclaimable_jobs()
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
   WHERE (j.status = 'queued'
          OR (j.status IN ('running', 'cancelling') AND j.lease_until < now()))
     AND NOT EXISTS (
       SELECT 1 FROM library_grants g
        WHERE g.library_id = j.library_id AND g.user_id = j.requested_by
          AND role_rank(g.role) >= role_rank('editor'));
  GET DIAGNOSTICS failed = ROW_COUNT;
  RETURN failed;
END;
$function$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION fail_unclaimable_jobs()
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
