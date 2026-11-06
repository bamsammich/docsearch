-- Version 28: adding someone to a library invites them, and they accept.
--
-- An admin granting a role to someone with none gave it at once, so the
-- library reached that person's searches, and their agents' searches,
-- before they knew of it. A library's admin could put text in front of
-- someone else's agent that way.
--
-- Now granting a role to someone with none records an invitation instead.
-- An invitation is not a grant: nothing that decides what a user reads
-- looks at it, so row-level security, search and the worker are unchanged.
-- The invitee accepts it, which makes the grant, or declines it. An admin
-- can change an invitation's role, or withdraw it. Granting to someone who
-- already holds a role changes the role, as before.
--
-- Every change is recorded in access_events, which gains four actions:
--
--   invite    an admin invited the subject with a role
--   accept    the subject accepted, taking that role
--   decline   the subject declined
--   withdraw  an admin withdrew the subject's invitation
--
-- A missing invitation is refused with its own SQLSTATE, DSL05, in the same
-- words whether or not the library exists.
--
-- Grants made before this version stand: they were given, and are kept.
--
-- +goose Up
-- +goose StatementBegin
CREATE TABLE library_invitations (
  library_id text NOT NULL REFERENCES libraries(library_id) ON DELETE CASCADE,
  user_id    text NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('reader', 'editor', 'admin')),
  invited_by text REFERENCES users(user_id) ON DELETE SET NULL,
  invited_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (library_id, user_id)
);
CREATE INDEX library_invitations_user ON library_invitations(user_id);

ALTER TABLE access_events DROP CONSTRAINT access_events_action_check;
ALTER TABLE access_events ADD CONSTRAINT access_events_action_check
  CHECK (action IN ('create', 'grant', 'revoke', 'invite', 'accept', 'decline', 'withdraw'));
-- +goose StatementEnd

-- +goose StatementBegin
-- grant_library changes the role of someone who holds one, and invites
-- anyone else. It answers which it did: 'granted' or 'invited'.
DROP FUNCTION grant_library(text, text, text);
CREATE FUNCTION grant_library(lib_slug text, member_username text, new_role text)
RETURNS text
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
  IF NOT EXISTS (SELECT 1 FROM library_grants WHERE library_id = lib AND user_id = member) THEN
    INSERT INTO library_invitations (library_id, user_id, role, invited_by)
      VALUES (lib, member, new_role, actor)
    ON CONFLICT (library_id, user_id) DO UPDATE
      SET role = EXCLUDED.role, invited_by = EXCLUDED.invited_by, invited_at = now();
    INSERT INTO access_events (library_id, actor, action, subject, role)
      VALUES (lib, actor, 'invite', member, new_role);
    RETURN 'invited';
  END IF;
  IF admins_left(lib, member, new_role) = 0 THEN
    RAISE EXCEPTION 'that would leave % without an admin', lib_slug USING ERRCODE = 'DSL03';
  END IF;
  UPDATE library_grants SET role = new_role, granted_by = actor, granted_at = now()
   WHERE library_id = lib AND user_id = member;
  INSERT INTO access_events (library_id, actor, action, subject, role)
    VALUES (lib, actor, 'grant', member, new_role);
  RETURN 'granted';
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- revoke_library takes a member's role away, or withdraws an invitation. An
-- admin may do either to anyone, and a member may revoke their own role,
-- which is how they leave.
CREATE OR REPLACE FUNCTION revoke_library(lib_slug text, member_username text)
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
  DELETE FROM library_invitations WHERE library_id = lib AND user_id = member;
  IF FOUND THEN
    INSERT INTO access_events (library_id, actor, action, subject)
      VALUES (lib, actor, 'withdraw', member);
    RETURN;
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
-- my_invitations are the transaction's user's open invitations, newest
-- first, with who sent each.
CREATE FUNCTION my_invitations()
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
-- +goose StatementEnd

-- +goose StatementBegin
-- invitation_for is the library a slug names, where the transaction's user
-- has an open invitation, and NULL otherwise.
CREATE FUNCTION invitation_for(lib_slug text)
RETURNS text
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT i.library_id
    FROM library_invitations i JOIN libraries l ON l.library_id = i.library_id
   WHERE l.slug = lib_slug AND i.user_id = current_setting('app.user_id', true);
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- accept_invitation turns the user's invitation into the role it offered,
-- and answers that role.
CREATE FUNCTION accept_invitation(lib_slug text)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor  text := current_setting('app.user_id', true);
  lib    text := invitation_for(lib_slug);
  offer  library_invitations;
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no invitation to a library named %', lib_slug USING ERRCODE = 'DSL05';
  END IF;
  DELETE FROM library_invitations WHERE library_id = lib AND user_id = actor
    RETURNING * INTO offer;
  INSERT INTO library_grants (library_id, user_id, role, granted_by)
    VALUES (lib, actor, offer.role, offer.invited_by)
  ON CONFLICT (library_id, user_id) DO NOTHING;
  INSERT INTO access_events (library_id, actor, action, subject, role)
    VALUES (lib, actor, 'accept', actor, offer.role);
  RETURN offer.role;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION decline_invitation(lib_slug text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor text := current_setting('app.user_id', true);
  lib   text := invitation_for(lib_slug);
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no invitation to a library named %', lib_slug USING ERRCODE = 'DSL05';
  END IF;
  DELETE FROM library_invitations WHERE library_id = lib AND user_id = actor;
  INSERT INTO access_events (library_id, actor, action, subject)
    VALUES (lib, actor, 'decline', actor);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- library_members is everyone with a role on a library the user reads. An
-- admin also sees who is invited, marked invited.
DROP FUNCTION library_members(text);
CREATE FUNCTION library_members(lib_slug text)
RETURNS TABLE (username text, role text, invited boolean)
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
    SELECT m.who, m.held, m.pending
      FROM (SELECT COALESCE(u.username, ''), g.role, false
              FROM library_grants g JOIN users u ON u.user_id = g.user_id
             WHERE g.library_id = lib
            UNION ALL
            SELECT COALESCE(u.username, ''), i.role, true
              FROM library_invitations i JOIN users u ON u.user_id = i.user_id
             WHERE i.library_id = lib AND library_by_slug(lib_slug, 'admin') IS NOT NULL
           ) AS m(who, held, pending)
     ORDER BY m.pending, role_rank(m.held) DESC, m.who;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- A removed password account's open invitations go with its roles.
CREATE OR REPLACE FUNCTION password_account_removed()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  stranded text;
BEGIN
  SELECT string_agg(l.name, ', ' ORDER BY l.name) INTO stranded
    FROM library_grants g JOIN libraries l ON l.library_id = g.library_id
   WHERE g.user_id = OLD.user_id AND g.role = 'admin' AND l.kind = 'shared'
     AND NOT EXISTS (SELECT 1 FROM library_grants other
                      WHERE other.library_id = g.library_id AND other.role = 'admin'
                        AND other.user_id <> OLD.user_id);
  IF stranded IS NOT NULL THEN
    RAISE EXCEPTION 'removing % would leave % without an admin: make someone else its admin first',
      OLD.username, stranded USING ERRCODE = 'DSA04';
  END IF;
  INSERT INTO access_events (library_id, actor, action, subject)
    SELECT g.library_id, NULLIF(current_setting('app.user_id', true), ''), 'revoke', OLD.user_id
      FROM library_grants g JOIN libraries l ON l.library_id = g.library_id
     WHERE g.user_id = OLD.user_id AND l.kind = 'shared';
  DELETE FROM library_grants g USING libraries l
   WHERE l.library_id = g.library_id AND g.user_id = OLD.user_id AND l.kind = 'shared';
  INSERT INTO access_events (library_id, actor, action, subject)
    SELECT i.library_id, NULLIF(current_setting('app.user_id', true), ''), 'withdraw', OLD.user_id
      FROM library_invitations i WHERE i.user_id = OLD.user_id;
  DELETE FROM library_invitations WHERE user_id = OLD.user_id;
  UPDATE users SET username = NULL WHERE user_id = OLD.user_id;
  RETURN OLD;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $grants$
DECLARE
  fn text;
BEGIN
  FOREACH fn IN ARRAY ARRAY[
    'grant_library(text, text, text)', 'library_members(text)', 'my_invitations()',
    'accept_invitation(text)', 'decline_invitation(text)'
  ] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', fn);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO docsearch_app', fn);
  END LOOP;
  REVOKE ALL ON FUNCTION invitation_for(text) FROM PUBLIC;
END;
$grants$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION decline_invitation(text);
DROP FUNCTION accept_invitation(text);
DROP FUNCTION invitation_for(text);
DROP FUNCTION my_invitations();

DROP FUNCTION library_members(text);
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

DROP FUNCTION grant_library(text, text, text);
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

CREATE OR REPLACE FUNCTION revoke_library(lib_slug text, member_username text)
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

CREATE OR REPLACE FUNCTION password_account_removed()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  stranded text;
BEGIN
  SELECT string_agg(l.name, ', ' ORDER BY l.name) INTO stranded
    FROM library_grants g JOIN libraries l ON l.library_id = g.library_id
   WHERE g.user_id = OLD.user_id AND g.role = 'admin' AND l.kind = 'shared'
     AND NOT EXISTS (SELECT 1 FROM library_grants other
                      WHERE other.library_id = g.library_id AND other.role = 'admin'
                        AND other.user_id <> OLD.user_id);
  IF stranded IS NOT NULL THEN
    RAISE EXCEPTION 'removing % would leave % without an admin: make someone else its admin first',
      OLD.username, stranded USING ERRCODE = 'DSA04';
  END IF;
  INSERT INTO access_events (library_id, actor, action, subject)
    SELECT g.library_id, NULLIF(current_setting('app.user_id', true), ''), 'revoke', OLD.user_id
      FROM library_grants g JOIN libraries l ON l.library_id = g.library_id
     WHERE g.user_id = OLD.user_id AND l.kind = 'shared';
  DELETE FROM library_grants g USING libraries l
   WHERE l.library_id = g.library_id AND g.user_id = OLD.user_id AND l.kind = 'shared';
  UPDATE users SET username = NULL WHERE user_id = OLD.user_id;
  RETURN OLD;
END;
$function$;

DELETE FROM access_events WHERE action IN ('invite', 'accept', 'decline', 'withdraw');
ALTER TABLE access_events DROP CONSTRAINT access_events_action_check;
ALTER TABLE access_events ADD CONSTRAINT access_events_action_check
  CHECK (action IN ('create', 'grant', 'revoke'));
DROP TABLE library_invitations;

REVOKE ALL ON FUNCTION grant_library(text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION grant_library(text, text, text) TO docsearch_app;
REVOKE ALL ON FUNCTION library_members(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION library_members(text) TO docsearch_app;
-- +goose StatementEnd
