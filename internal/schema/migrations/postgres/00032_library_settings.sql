-- Version 32: an admin renames a shared library, deletes it, and can bring
-- it back for 14 days.
--
-- Renaming changes the name people read. The slug stays, since agents,
-- stored doc ids such as platform/install-guide and upload links name the
-- library by it.
--
-- Deleting marks the library deleted. From then on every function that
-- decides who reads or writes a library skips it, so it leaves every
-- search, list and queue at once, and its queued and running jobs fail.
-- Its rows, grants and slug are kept for library_grace(), 14 days, during
-- which any of its admins may restore it. After that the worker purges it:
-- its chunks partition is dropped and every row it owned is deleted. A
-- personal library has no slug, so none of these functions reach one.
--
-- The purge runs as the owner role, which row-level security binds too.
-- Each tenant table gains purge policies that let the owner read and
-- delete only the rows of a library past its grace period. A DELETE with a
-- WHERE clause needs the SELECT policy as well as the DELETE one.
--
-- access_events gains rename, delete and restore, and a detail column that
-- keeps a rename's old name.
--
-- +goose Up
-- +goose StatementBegin
ALTER TABLE libraries
  ADD COLUMN deleted_at timestamptz,
  ADD COLUMN deleted_by text REFERENCES users(user_id) ON DELETE SET NULL;

ALTER TABLE access_events ADD COLUMN detail text;
ALTER TABLE access_events DROP CONSTRAINT access_events_action_check;
ALTER TABLE access_events ADD CONSTRAINT access_events_action_check
  CHECK (action IN ('create', 'grant', 'revoke', 'invite', 'accept', 'decline', 'withdraw',
                    'rename', 'delete', 'restore'));
-- +goose StatementEnd

-- +goose StatementBegin
-- library_grace is how long a deleted library can be restored.
CREATE FUNCTION library_grace()
RETURNS interval
LANGUAGE sql IMMUTABLE
AS $function$ SELECT interval '14 days' $function$;

-- library_purgeable reports whether a library is past its grace period.
CREATE FUNCTION library_purgeable(lib text)
RETURNS boolean
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT EXISTS (SELECT 1 FROM libraries
                  WHERE library_id = lib AND deleted_at < now() - library_grace());
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $policies$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['documents', 'pages', 'index_terms', 'responses', 'robots', 'uploads'] LOOP
    EXECUTE format('CREATE POLICY purge_read ON %I FOR SELECT TO docsearch_owner USING (library_purgeable(library_id))', t);
    EXECUTE format('CREATE POLICY purge ON %I FOR DELETE TO docsearch_owner USING (library_purgeable(library_id))', t);
  END LOOP;
END;
$policies$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app_libraries(min_role text)
RETURNS text[]
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT COALESCE(array_agg(g.library_id), '{}')
    FROM library_grants g JOIN libraries l ON l.library_id = g.library_id
   WHERE g.user_id = current_setting('app.user_id', true)
     AND role_rank(g.role) >= role_rank(min_role)
     AND l.deleted_at IS NULL;
$function$;

CREATE OR REPLACE FUNCTION library_by_slug(lib_slug text, min_role text)
RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT l.library_id
    FROM libraries l
    JOIN library_grants g ON g.library_id = l.library_id
   WHERE l.slug = lib_slug
     AND l.deleted_at IS NULL
     AND g.user_id = current_setting('app.user_id', true)
     AND role_rank(g.role) >= role_rank(min_role);
$function$;

CREATE OR REPLACE FUNCTION readable_libraries()
RETURNS TABLE (library_id text, kind text, slug text, name text, role text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT l.library_id, l.kind, l.slug, l.name, g.role
    FROM library_grants g
    JOIN libraries l ON l.library_id = g.library_id
   WHERE g.user_id = current_setting('app.user_id', true)
     AND l.deleted_at IS NULL
   ORDER BY (l.library_id = g.user_id) DESC, l.slug NULLS LAST, l.library_id;
$function$;

CREATE OR REPLACE FUNCTION waiting_queues()
RETURNS TABLE (library_id text, requested_by text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT DISTINCT j.library_id, j.requested_by
    FROM ingest_jobs j
    JOIN library_grants g ON g.library_id = j.library_id AND g.user_id = j.requested_by
    JOIN libraries l ON l.library_id = j.library_id
   WHERE role_rank(g.role) >= role_rank('editor')
     AND l.deleted_at IS NULL
     AND (j.status = 'queued' OR (j.status = 'running' AND j.lease_until < now()))
   ORDER BY j.library_id, j.requested_by;
$function$;

CREATE OR REPLACE FUNCTION my_invitations()
RETURNS TABLE (slug text, name text, role text, invited_by text, invited_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT l.slug, l.name, i.role, COALESCE(u.username, ''), i.invited_at
    FROM library_invitations i
    JOIN libraries l ON l.library_id = i.library_id
    LEFT JOIN users u ON u.user_id = i.invited_by
   WHERE i.user_id = current_setting('app.user_id', true)
     AND l.deleted_at IS NULL
   ORDER BY i.invited_at DESC, l.slug;
$function$;

CREATE OR REPLACE FUNCTION invitation_for(lib_slug text)
RETURNS text
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT i.library_id
    FROM library_invitations i JOIN libraries l ON l.library_id = i.library_id
   WHERE l.slug = lib_slug AND l.deleted_at IS NULL
     AND i.user_id = current_setting('app.user_id', true);
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- library_events now answers each event's detail, a rename's old name.
DROP FUNCTION library_events(text);
CREATE FUNCTION library_events(lib_slug text)
RETURNS TABLE (at timestamptz, actor text, action text, subject text, role text, detail text)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  lib text := library_by_slug(lib_slug, 'admin');
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  RETURN QUERY
    SELECT e.at, COALESCE(a.username, e.actor_name, ''), e.action,
           COALESCE(s.username, e.subject_name, ''), COALESCE(e.role, ''), COALESCE(e.detail, '')
      FROM access_events e
      LEFT JOIN users a ON a.user_id = e.actor
      LEFT JOIN users s ON s.user_id = e.subject
     WHERE e.library_id = lib
     ORDER BY e.at, e.id;
END;
$function$;
REVOKE ALL ON FUNCTION library_events(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION library_events(text) TO docsearch_app;
-- +goose StatementEnd

-- +goose StatementBegin
-- rename_library changes a shared library's name. Admins only.
CREATE FUNCTION rename_library(lib_slug text, new_name text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor text := current_setting('app.user_id', true);
  lib   text := library_by_slug(lib_slug, 'admin');
  old   text;
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  IF btrim(coalesce(new_name, '')) = '' THEN
    RAISE EXCEPTION 'a library needs a name' USING ERRCODE = '22023';
  END IF;
  SELECT name INTO old FROM libraries WHERE library_id = lib;
  IF old = btrim(new_name) THEN
    RETURN;
  END IF;
  UPDATE libraries SET name = btrim(new_name) WHERE library_id = lib;
  INSERT INTO access_events (library_id, actor, action, subject, detail)
    VALUES (lib, actor, 'rename', actor, old);
END;
$function$;

-- delete_library marks a shared library deleted, fails its jobs, and
-- answers when it will be purged. Admins only.
CREATE FUNCTION delete_library(lib_slug text)
RETURNS timestamptz
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor text := current_setting('app.user_id', true);
  lib   text := library_by_slug(lib_slug, 'admin');
  at    timestamptz := now();
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  UPDATE libraries SET deleted_at = at, deleted_by = actor WHERE library_id = lib;
  UPDATE ingest_jobs
     SET status = 'failed', permanent = true, lease_until = NULL, updated_at = at,
         error = 'the library was deleted'
   WHERE library_id = lib AND status IN ('queued', 'running', 'cancelling');
  INSERT INTO access_events (library_id, actor, action, subject)
    VALUES (lib, actor, 'delete', actor);
  RETURN at + library_grace();
END;
$function$;

-- deleted_libraries are the deleted libraries the user administers that
-- can still be restored, newest first.
CREATE FUNCTION deleted_libraries()
RETURNS TABLE (slug text, name text, deleted_at timestamptz, purge_at timestamptz, deleted_by text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT l.slug, l.name, l.deleted_at, l.deleted_at + library_grace(), COALESCE(u.username, '')
    FROM libraries l
    JOIN library_grants g ON g.library_id = l.library_id
    LEFT JOIN users u ON u.user_id = l.deleted_by
   WHERE g.user_id = current_setting('app.user_id', true) AND g.role = 'admin'
     AND l.deleted_at IS NOT NULL AND NOT library_purgeable(l.library_id)
   ORDER BY l.deleted_at DESC, l.slug;
$function$;

-- restore_library brings back a deleted library within its grace period.
-- Any of its admins may. Its failed jobs stay failed.
CREATE FUNCTION restore_library(lib_slug text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor text := current_setting('app.user_id', true);
  lib   text;
BEGIN
  SELECT l.library_id INTO lib
    FROM libraries l JOIN library_grants g ON g.library_id = l.library_id
   WHERE l.slug = lib_slug AND l.deleted_at IS NOT NULL AND NOT library_purgeable(l.library_id)
     AND g.user_id = actor AND g.role = 'admin';
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no deleted library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  UPDATE libraries SET deleted_at = NULL, deleted_by = NULL WHERE library_id = lib;
  INSERT INTO access_events (library_id, actor, action, subject)
    VALUES (lib, actor, 'restore', actor);
END;
$function$;

-- purge_deleted_libraries removes every library past its grace period and
-- everything it owned, and answers how many it removed.
CREATE FUNCTION purge_deleted_libraries()
RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  lib    text;
  purged integer := 0;
BEGIN
  FOR lib IN SELECT library_id FROM libraries
              WHERE kind = 'shared' AND library_purgeable(library_id) LOOP
    EXECUTE format('DROP TABLE IF EXISTS %I', 'chunks_' || lib);
    DELETE FROM index_terms WHERE library_id = lib;
    DELETE FROM pages WHERE library_id = lib;
    DELETE FROM documents WHERE library_id = lib;
    DELETE FROM ingest_jobs WHERE library_id = lib;
    DELETE FROM responses WHERE library_id = lib;
    DELETE FROM robots WHERE library_id = lib;
    DELETE FROM upload_links WHERE library_id = lib;
    DELETE FROM uploads WHERE library_id = lib;
    DELETE FROM libraries WHERE library_id = lib;
    purged := purged + 1;
  END LOOP;
  RETURN purged;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $grants$
DECLARE
  fn text;
BEGIN
  FOREACH fn IN ARRAY ARRAY[
    'rename_library(text, text)', 'delete_library(text)', 'deleted_libraries()',
    'restore_library(text)', 'purge_deleted_libraries()'
  ] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', fn);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO docsearch_app', fn);
  END LOOP;
  REVOKE ALL ON FUNCTION library_purgeable(text) FROM PUBLIC;
END;
$grants$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION library_events(text);
CREATE FUNCTION library_events(lib_slug text)
RETURNS TABLE (at timestamptz, actor text, action text, subject text, role text)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  lib text := library_by_slug(lib_slug, 'admin');
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  RETURN QUERY
    SELECT e.at, COALESCE(a.username, e.actor_name, ''), e.action,
           COALESCE(s.username, e.subject_name, ''), COALESCE(e.role, '')
      FROM access_events e
      LEFT JOIN users a ON a.user_id = e.actor
      LEFT JOIN users s ON s.user_id = e.subject
     WHERE e.library_id = lib
     ORDER BY e.at, e.id;
END;
$function$;
REVOKE ALL ON FUNCTION library_events(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION library_events(text) TO docsearch_app;

DROP FUNCTION purge_deleted_libraries();
DROP FUNCTION restore_library(text);
DROP FUNCTION deleted_libraries();
DROP FUNCTION delete_library(text);
DROP FUNCTION rename_library(text, text);

CREATE OR REPLACE FUNCTION invitation_for(lib_slug text)
RETURNS text
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT i.library_id
    FROM library_invitations i JOIN libraries l ON l.library_id = i.library_id
   WHERE l.slug = lib_slug AND i.user_id = current_setting('app.user_id', true);
$function$;

CREATE OR REPLACE FUNCTION my_invitations()
RETURNS TABLE (slug text, name text, role text, invited_by text, invited_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT l.slug, l.name, i.role, COALESCE(u.username, ''), i.invited_at
    FROM library_invitations i
    JOIN libraries l ON l.library_id = i.library_id
    LEFT JOIN users u ON u.user_id = i.invited_by
   WHERE i.user_id = current_setting('app.user_id', true)
   ORDER BY i.invited_at DESC, l.slug;
$function$;

CREATE OR REPLACE FUNCTION waiting_queues()
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

CREATE OR REPLACE FUNCTION readable_libraries()
RETURNS TABLE (library_id text, kind text, slug text, name text, role text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT l.library_id, l.kind, l.slug, l.name, g.role
    FROM library_grants g
    JOIN libraries l ON l.library_id = g.library_id
   WHERE g.user_id = current_setting('app.user_id', true)
   ORDER BY (l.library_id = g.user_id) DESC, l.slug NULLS LAST, l.library_id;
$function$;

CREATE OR REPLACE FUNCTION library_by_slug(lib_slug text, min_role text)
RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT l.library_id
    FROM libraries l
    JOIN library_grants g ON g.library_id = l.library_id
   WHERE l.slug = lib_slug
     AND g.user_id = current_setting('app.user_id', true)
     AND role_rank(g.role) >= role_rank(min_role);
$function$;

CREATE OR REPLACE FUNCTION app_libraries(min_role text)
RETURNS text[]
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT COALESCE(array_agg(library_id), '{}')
    FROM library_grants
   WHERE user_id = current_setting('app.user_id', true)
     AND role_rank(role) >= role_rank(min_role);
$function$;

DO $policies$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['documents', 'pages', 'index_terms', 'responses', 'robots', 'uploads'] LOOP
    EXECUTE format('DROP POLICY purge ON %I', t);
    EXECUTE format('DROP POLICY purge_read ON %I', t);
  END LOOP;
END;
$policies$;
DROP FUNCTION library_purgeable(text);
DROP FUNCTION library_grace();

DELETE FROM access_events WHERE action IN ('rename', 'delete', 'restore');
ALTER TABLE access_events DROP CONSTRAINT access_events_action_check;
ALTER TABLE access_events ADD CONSTRAINT access_events_action_check
  CHECK (action IN ('create', 'grant', 'revoke', 'invite', 'accept', 'decline', 'withdraw'));
ALTER TABLE access_events DROP COLUMN detail;
ALTER TABLE libraries DROP COLUMN deleted_by, DROP COLUMN deleted_at;
-- +goose StatementEnd
