-- Version 30: closes the gaps a security review found below the application.
--
-- Partitions. Each library's chunks live in a partition the request role
-- was granted directly, and row-level security was enabled only on the
-- parent, so a query naming chunks_<library> read any library's text.
-- Nothing in docsearch names a partition. Row-level security is now enabled
-- on every partition, with no policy of its own, so naming one directly
-- returns nothing. A query through chunks still answers to the parent's
-- policies, which is how every query reaches them.
--
-- set_username. It renamed any account it was handed. It now refuses the
-- request role unless the account is the one the transaction names, the
-- same rule every other definer keeps. The owner role, which the CLI and
-- tests use, may still name anyone.
--
-- Libraries. One account may create at most 100 shared libraries, since
-- each makes a table and an index; the 101st is refused with DSL06. A
-- library's name is at most 120 characters. Libraries already over the
-- limit are kept.
--
-- The change log. access_events recorded people by user_id alone, so a
-- removed account, whose username is cleared, read as "someone". Each
-- event now keeps the usernames of its actor and subject as they were when
-- it happened, and library_events falls back on them.
--
-- +goose Up
-- +goose StatementBegin
DO $partitions$
DECLARE
  part regclass;
BEGIN
  FOR part IN SELECT inhrelid::regclass FROM pg_inherits WHERE inhparent = 'chunks'::regclass LOOP
    EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', part);
  END LOOP;
END;
$partitions$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION create_account(new_user_id text, new_issuer text DEFAULT NULL, new_subject text DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  partition_name text := 'chunks_' || new_user_id;
BEGIN
  INSERT INTO users (user_id, issuer, subject)
    VALUES (new_user_id, new_issuer, new_subject);
  INSERT INTO libraries (library_id, kind, created_by)
    VALUES (new_user_id, 'personal', new_user_id);
  INSERT INTO library_grants (library_id, user_id, role)
    VALUES (new_user_id, new_user_id, 'admin');
  EXECUTE format('CREATE TABLE %I PARTITION OF chunks FOR VALUES IN (%L)',
                 partition_name, new_user_id);
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', partition_name);
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO docsearch_app',
                 partition_name);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE libraries ADD CONSTRAINT libraries_name_length
  CHECK (char_length(name) <= 120) NOT VALID;

CREATE OR REPLACE FUNCTION create_library(new_slug text, new_name text)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor  text := current_setting('app.user_id', true);
  new_id text := 'lib_' || substr(md5(random()::text || clock_timestamp()::text), 1, 20);
BEGIN
  IF actor IS NULL OR actor = '' THEN
    RAISE EXCEPTION 'no user' USING ERRCODE = 'DSL01';
  END IF;
  IF (SELECT count(*) FROM libraries WHERE created_by = actor AND kind = 'shared') >= 100 THEN
    RAISE EXCEPTION 'you have created 100 libraries, the most one account may' USING ERRCODE = 'DSL06';
  END IF;
  IF EXISTS (SELECT 1 FROM libraries WHERE slug = new_slug) THEN
    RAISE EXCEPTION 'a library named % already exists', new_slug USING ERRCODE = 'DSL04';
  END IF;
  INSERT INTO libraries (library_id, kind, slug, name, created_by)
    VALUES (new_id, 'shared', new_slug, new_name, actor);
  EXECUTE format('CREATE TABLE %I PARTITION OF chunks FOR VALUES IN (%L)',
                 'chunks_' || new_id, new_id);
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', 'chunks_' || new_id);
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO docsearch_app',
                 'chunks_' || new_id);
  INSERT INTO library_grants (library_id, user_id, role, granted_by)
    VALUES (new_id, actor, 'admin', actor);
  INSERT INTO access_events (library_id, actor, action, subject, role)
    VALUES (new_id, actor, 'create', actor, 'admin');
  RETURN new_id;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION set_username(target_user text, new_username text)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
BEGIN
  IF session_user <> current_user
     AND target_user IS DISTINCT FROM NULLIF(current_setting('app.user_id', true), '') THEN
    RAISE EXCEPTION 'an account names only itself' USING ERRCODE = '42501';
  END IF;
  UPDATE users SET username = new_username
   WHERE user_id = target_user
     AND username IS DISTINCT FROM new_username
     AND NOT EXISTS (SELECT 1 FROM users other
                      WHERE other.username = new_username AND other.user_id <> target_user);
  RETURN EXISTS (SELECT 1 FROM users WHERE user_id = target_user AND username = new_username);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE access_events ADD COLUMN actor_name text, ADD COLUMN subject_name text;
UPDATE access_events e
   SET actor_name = (SELECT username FROM users WHERE user_id = e.actor),
       subject_name = (SELECT username FROM users WHERE user_id = e.subject);

CREATE FUNCTION access_event_names()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $function$
BEGIN
  NEW.actor_name := COALESCE(NEW.actor_name, (SELECT username FROM users WHERE user_id = NEW.actor));
  NEW.subject_name := COALESCE(NEW.subject_name, (SELECT username FROM users WHERE user_id = NEW.subject));
  RETURN NEW;
END;
$function$;
CREATE TRIGGER access_event_names BEFORE INSERT ON access_events
  FOR EACH ROW EXECUTE FUNCTION access_event_names();

CREATE OR REPLACE FUNCTION library_events(lib_slug text)
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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION library_events(lib_slug text)
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
    SELECT e.at, COALESCE(a.username, ''), e.action, COALESCE(s.username, ''), COALESCE(e.role, '')
      FROM access_events e
      LEFT JOIN users a ON a.user_id = e.actor
      LEFT JOIN users s ON s.user_id = e.subject
     WHERE e.library_id = lib
     ORDER BY e.at, e.id;
END;
$function$;
DROP TRIGGER access_event_names ON access_events;
DROP FUNCTION access_event_names();
ALTER TABLE access_events DROP COLUMN actor_name, DROP COLUMN subject_name;

CREATE OR REPLACE FUNCTION set_username(target_user text, new_username text)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
BEGIN
  UPDATE users SET username = new_username
   WHERE user_id = target_user
     AND username IS DISTINCT FROM new_username
     AND NOT EXISTS (SELECT 1 FROM users other
                      WHERE other.username = new_username AND other.user_id <> target_user);
  RETURN EXISTS (SELECT 1 FROM users WHERE user_id = target_user AND username = new_username);
END;
$function$;

CREATE OR REPLACE FUNCTION create_library(new_slug text, new_name text)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor  text := current_setting('app.user_id', true);
  new_id text := 'lib_' || substr(md5(random()::text || clock_timestamp()::text), 1, 20);
BEGIN
  IF actor IS NULL OR actor = '' THEN
    RAISE EXCEPTION 'no user' USING ERRCODE = 'DSL01';
  END IF;
  IF EXISTS (SELECT 1 FROM libraries WHERE slug = new_slug) THEN
    RAISE EXCEPTION 'a library named % already exists', new_slug USING ERRCODE = 'DSL04';
  END IF;
  INSERT INTO libraries (library_id, kind, slug, name, created_by)
    VALUES (new_id, 'shared', new_slug, new_name, actor);
  EXECUTE format('CREATE TABLE %I PARTITION OF chunks FOR VALUES IN (%L)',
                 'chunks_' || new_id, new_id);
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO docsearch_app',
                 'chunks_' || new_id);
  INSERT INTO library_grants (library_id, user_id, role, granted_by)
    VALUES (new_id, actor, 'admin', actor);
  INSERT INTO access_events (library_id, actor, action, subject, role)
    VALUES (new_id, actor, 'create', actor, 'admin');
  RETURN new_id;
END;
$function$;
ALTER TABLE libraries DROP CONSTRAINT libraries_name_length;

CREATE OR REPLACE FUNCTION create_account(new_user_id text, new_issuer text DEFAULT NULL, new_subject text DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  partition_name text := 'chunks_' || new_user_id;
BEGIN
  INSERT INTO users (user_id, issuer, subject)
    VALUES (new_user_id, new_issuer, new_subject);
  INSERT INTO libraries (library_id, kind, created_by)
    VALUES (new_user_id, 'personal', new_user_id);
  INSERT INTO library_grants (library_id, user_id, role)
    VALUES (new_user_id, new_user_id, 'admin');
  EXECUTE format('CREATE TABLE %I PARTITION OF chunks FOR VALUES IN (%L)',
                 partition_name, new_user_id);
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO docsearch_app',
                 partition_name);
END;
$function$;

DO $partitions$
DECLARE
  part regclass;
BEGIN
  FOR part IN SELECT inhrelid::regclass FROM pg_inherits WHERE inhparent = 'chunks'::regclass LOOP
    EXECUTE format('ALTER TABLE %s DISABLE ROW LEVEL SECURITY', part);
  END LOOP;
END;
$partitions$;
-- +goose StatementEnd
