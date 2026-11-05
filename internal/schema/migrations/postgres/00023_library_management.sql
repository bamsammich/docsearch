-- Version 23: anyone signed in can create a library, and its admins share it.
--
-- The request role cannot write libraries or library_grants, which is what
-- keeps one user from granting themselves another's library. Every change
-- goes through a definer function that acts for the user the transaction
-- named, checks that user's role first, and records what it did in
-- access_events in the same transaction.
--
-- The rules are the database's, so every door that manages libraries gets
-- the same ones:
--
--   anyone signed in may create a library, and becomes its admin
--   only an admin grants, changes or revokes a role
--   anyone may leave a library, by revoking their own grant
--   a library always keeps an admin: the change that would leave none is refused
--   only someone who reads a library sees its members, and only an admin its events
--
-- A library the user cannot act on is refused in the same words whether or
-- not it exists. Each refusal carries its own SQLSTATE, so the application
-- can answer it without reading the message:
--
--   DSL01  no library by that name that you can do this to
--   DSL02  no account by that username
--   DSL03  the change would leave the library without an admin
--   DSL04  the slug is taken
--
-- +goose Up
-- +goose StatementBegin
-- "personal" is how a user names their own library, so no shared library may
-- take it.
ALTER TABLE libraries ADD CONSTRAINT libraries_slug_not_personal CHECK (slug <> 'personal');

CREATE TABLE access_events (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  at         timestamptz NOT NULL DEFAULT now(),
  library_id text NOT NULL REFERENCES libraries(library_id) ON DELETE CASCADE,
  actor      text REFERENCES users(user_id) ON DELETE SET NULL,
  action     text NOT NULL CHECK (action IN ('create', 'grant', 'revoke')),
  subject    text REFERENCES users(user_id) ON DELETE SET NULL,
  role       text
);
CREATE INDEX access_events_library ON access_events(library_id, at);
-- +goose StatementEnd

-- +goose StatementBegin
-- library_by_slug is the library a slug names, where the transaction's user
-- holds at least min_role on it, and NULL otherwise.
CREATE FUNCTION library_by_slug(lib_slug text, min_role text)
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
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION create_library(new_slug text, new_name text)
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
-- +goose StatementEnd

-- +goose StatementBegin
-- admins_left is how many admins a library would keep if member's grant
-- became new_role. A NULL role is the grant taken away.
CREATE FUNCTION admins_left(lib text, member text, new_role text)
RETURNS bigint
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT (SELECT COUNT(*) FROM library_grants
           WHERE library_id = lib AND role = 'admin' AND user_id <> member)
         + CASE WHEN new_role = 'admin' THEN 1 ELSE 0 END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION grant_library(lib_slug text, member_username text, new_role text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor  text := current_setting('app.user_id', true);
  lib    text := library_by_slug(lib_slug, 'admin');
  member text;
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  SELECT user_id INTO member FROM users WHERE username = member_username;
  IF member IS NULL THEN
    RAISE EXCEPTION 'no account named %', member_username USING ERRCODE = 'DSL02';
  END IF;
  IF role_rank(new_role) IS NULL THEN
    RAISE EXCEPTION 'a role is reader, editor or admin' USING ERRCODE = '22023';
  END IF;
  IF admins_left(lib, member, new_role) = 0 THEN
    RAISE EXCEPTION 'that would leave % without an admin', lib_slug USING ERRCODE = 'DSL03';
  END IF;
  INSERT INTO library_grants (library_id, user_id, role, granted_by)
    VALUES (lib, member, new_role, actor)
  ON CONFLICT (library_id, user_id) DO UPDATE
    SET role = EXCLUDED.role, granted_by = EXCLUDED.granted_by, granted_at = now();
  INSERT INTO access_events (library_id, actor, action, subject, role)
    VALUES (lib, actor, 'grant', member, new_role);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- revoke_library takes a member's grant away. An admin may revoke anyone's,
-- and anyone may revoke their own, which is how a member leaves.
CREATE FUNCTION revoke_library(lib_slug text, member_username text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor  text := current_setting('app.user_id', true);
  member text;
  lib    text;
BEGIN
  SELECT user_id INTO member FROM users WHERE username = member_username;
  IF member IS NOT NULL AND member = actor THEN
    lib := library_by_slug(lib_slug, 'reader');
  ELSE
    lib := library_by_slug(lib_slug, 'admin');
  END IF;
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  IF member IS NULL THEN
    RAISE EXCEPTION 'no account named %', member_username USING ERRCODE = 'DSL02';
  END IF;
  IF (SELECT kind FROM libraries WHERE library_id = lib) = 'personal' AND member = lib THEN
    RAISE EXCEPTION 'a personal library keeps its owner' USING ERRCODE = 'DSL03';
  END IF;
  IF admins_left(lib, member, NULL) = 0 THEN
    RAISE EXCEPTION 'that would leave % without an admin', lib_slug USING ERRCODE = 'DSL03';
  END IF;
  DELETE FROM library_grants WHERE library_id = lib AND user_id = member;
  IF FOUND THEN
    INSERT INTO access_events (library_id, actor, action, subject)
      VALUES (lib, actor, 'revoke', member);
  END IF;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION library_members(lib_slug text)
RETURNS TABLE (username text, role text)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  lib text := library_by_slug(lib_slug, 'reader');
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you read', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  RETURN QUERY
    SELECT COALESCE(u.username, ''), g.role
      FROM library_grants g JOIN users u ON u.user_id = g.user_id
     WHERE g.library_id = lib
     ORDER BY role_rank(g.role) DESC, u.username;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
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
    SELECT e.at, COALESCE(a.username, ''), e.action, COALESCE(s.username, ''), COALESCE(e.role, '')
      FROM access_events e
      LEFT JOIN users a ON a.user_id = e.actor
      LEFT JOIN users s ON s.user_id = e.subject
     WHERE e.library_id = lib
     ORDER BY e.at, e.id;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $grants$
DECLARE
  fn text;
BEGIN
  FOREACH fn IN ARRAY ARRAY['library_by_slug(text, text)', 'create_library(text, text)',
                            'grant_library(text, text, text)', 'revoke_library(text, text)',
                            'library_members(text)', 'library_events(text)'] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', fn);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO docsearch_app', fn);
  END LOOP;
END;
$grants$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION library_events(text);
DROP FUNCTION library_members(text);
DROP FUNCTION revoke_library(text, text);
DROP FUNCTION grant_library(text, text, text);
DROP FUNCTION admins_left(text, text, text);
DROP FUNCTION create_library(text, text);
DROP FUNCTION library_by_slug(text, text);
DROP TABLE access_events;
ALTER TABLE libraries DROP CONSTRAINT libraries_slug_not_personal;
-- +goose StatementEnd
