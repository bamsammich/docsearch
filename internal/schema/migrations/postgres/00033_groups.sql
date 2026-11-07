-- Version 33: groups, and libraries granted to a group.
--
-- A group is a set of accounts made in docsearch. Whoever creates one is
-- its first maintainer. A maintainer invites people by username, and each
-- accepts or declines, as with a library since version 28. A maintainer
-- may grant a library they administer to their group, and from then on
-- every member reaches the library with the group's role. A person's role
-- on a library is the highest of their own grant and each group's grant.
--
-- Every function that decides access now reads both paths through
-- user_library_roles: app_libraries, library_by_slug, readable_libraries,
-- waiting_queues and fail_unclaimable_jobs. Each still runs once per
-- statement, and app.library_id still prunes partitions.
--
-- The rules:
--
--   a group keeps a maintainer      the change that would leave none is
--                                   refused with DSG03
--   a library keeps a direct admin  admins_left still counts only
--                                   library_grants, so an admin grant to
--                                   a group never counts as the last one
--   who grants to a group           a library admin who maintains the
--                                   group, or a docsearch administrator
--   who sees a group                anyone signed in sees its name; its
--                                   members and administrators see who is
--                                   in it
--
-- Administrators may manage any group. A group holds no documents, so
-- deleting one removes its grants and leaves its libraries.
--
-- Removing a password account takes its memberships and invitations with
-- it. A group it alone maintained passes to the same heir as its libraries
-- do, or is left to the administrators when there is none.
--
-- provider_group is set by the provider link of a later version; nothing
-- here reads it.
--
-- New refusals, each with its own SQLSTATE:
--
--   DSG01  no group by that name that you may act on
--   DSG03  that would leave the group without a maintainer
--   DSG04  a group by that name already exists
--   DSG05  no invitation to a group by that name
--   DSG07  the membership comes from the provider group
--
-- +goose Up
-- +goose StatementBegin
CREATE TABLE groups (
  group_id       text PRIMARY KEY CHECK (group_id ~ '^grp_[a-z0-9]{1,36}$'),
  slug           text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9-]{1,40}$'),
  name           text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
  provider_group text UNIQUE,
  created_by     text REFERENCES users(user_id) ON DELETE SET NULL,
  created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE group_members (
  group_id text NOT NULL REFERENCES groups(group_id) ON DELETE CASCADE,
  user_id  text NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  role     text NOT NULL CHECK (role IN ('member', 'maintainer')),
  source   text NOT NULL CHECK (source IN ('invited', 'provider')),
  seen_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user ON group_members(user_id);

CREATE TABLE group_invitations (
  group_id   text NOT NULL REFERENCES groups(group_id) ON DELETE CASCADE,
  user_id    text NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('member', 'maintainer')),
  invited_by text REFERENCES users(user_id) ON DELETE SET NULL,
  invited_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_invitations_user ON group_invitations(user_id);

CREATE TABLE library_group_grants (
  library_id text NOT NULL REFERENCES libraries(library_id) ON DELETE CASCADE,
  group_id   text NOT NULL REFERENCES groups(group_id) ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('reader', 'editor', 'admin')),
  granted_by text REFERENCES users(user_id) ON DELETE SET NULL,
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (library_id, group_id)
);
CREATE INDEX library_group_grants_group ON library_group_grants(group_id);

CREATE TABLE group_events (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  at           timestamptz NOT NULL DEFAULT now(),
  group_id     text NOT NULL REFERENCES groups(group_id) ON DELETE CASCADE,
  actor        text REFERENCES users(user_id) ON DELETE SET NULL,
  actor_name   text,
  action       text NOT NULL CHECK (action IN ('create', 'rename', 'invite', 'accept', 'decline',
                                               'withdraw', 'role', 'remove')),
  subject      text REFERENCES users(user_id) ON DELETE SET NULL,
  subject_name text,
  role         text,
  detail       text
);
CREATE INDEX group_events_group ON group_events(group_id, at);
CREATE TRIGGER group_event_names BEFORE INSERT ON group_events
  FOR EACH ROW EXECUTE FUNCTION access_event_names();

ALTER TABLE access_events DROP CONSTRAINT access_events_action_check;
ALTER TABLE access_events ADD CONSTRAINT access_events_action_check
  CHECK (action IN ('create', 'grant', 'revoke', 'invite', 'accept', 'decline', 'withdraw',
                    'rename', 'delete', 'restore', 'group_grant', 'group_revoke'));
-- +goose StatementEnd

-- +goose StatementBegin
-- user_library_roles is every role uid holds on a library: their own
-- grants, and their groups' grants.
CREATE FUNCTION user_library_roles(uid text)
RETURNS TABLE (library_id text, role text)
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT g.library_id, g.role FROM library_grants g WHERE g.user_id = uid
  UNION ALL
  SELECT lg.library_id, lg.role
    FROM library_group_grants lg JOIN group_members m ON m.group_id = lg.group_id
   WHERE m.user_id = uid;
$function$;

CREATE OR REPLACE FUNCTION app_libraries(min_role text)
RETURNS text[]
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT COALESCE(array_agg(DISTINCT r.library_id), '{}')
    FROM user_library_roles(current_setting('app.user_id', true)) r
    JOIN libraries l ON l.library_id = r.library_id
   WHERE role_rank(r.role) >= role_rank(min_role) AND l.deleted_at IS NULL;
$function$;

CREATE OR REPLACE FUNCTION library_by_slug(lib_slug text, min_role text)
RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT l.library_id
    FROM libraries l
    JOIN user_library_roles(current_setting('app.user_id', true)) r ON r.library_id = l.library_id
   WHERE l.slug = lib_slug AND l.deleted_at IS NULL
     AND role_rank(r.role) >= role_rank(min_role)
   LIMIT 1;
$function$;

CREATE OR REPLACE FUNCTION readable_libraries()
RETURNS TABLE (library_id text, kind text, slug text, name text, role text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT l.library_id, l.kind, l.slug, l.name,
         (array_agg(r.role ORDER BY role_rank(r.role) DESC))[1]
    FROM user_library_roles(current_setting('app.user_id', true)) r
    JOIN libraries l ON l.library_id = r.library_id
   WHERE l.deleted_at IS NULL
   GROUP BY l.library_id, l.kind, l.slug, l.name
   ORDER BY (l.library_id = current_setting('app.user_id', true)) DESC, l.slug NULLS LAST, l.library_id;
$function$;

CREATE OR REPLACE FUNCTION waiting_queues()
RETURNS TABLE (library_id text, requested_by text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT DISTINCT j.library_id, j.requested_by
    FROM ingest_jobs j
    JOIN libraries l ON l.library_id = j.library_id
   WHERE l.deleted_at IS NULL
     AND (j.status = 'queued' OR (j.status = 'running' AND j.lease_until < now()))
     AND EXISTS (SELECT 1 FROM user_library_roles(j.requested_by) r
                  WHERE r.library_id = j.library_id AND role_rank(r.role) >= role_rank('editor'))
   ORDER BY j.library_id, j.requested_by;
$function$;

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
     AND NOT EXISTS (SELECT 1 FROM user_library_roles(j.requested_by) r
                      WHERE r.library_id = j.library_id
                        AND role_rank(r.role) >= role_rank('editor'));
  GET DIAGNOSTICS failed = ROW_COUNT;
  RETURN failed;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- caller_is_administrator reports whether the transaction's user is a
-- docsearch administrator.
CREATE FUNCTION caller_is_administrator()
RETURNS boolean
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT COALESCE((SELECT admin FROM users WHERE user_id = current_setting('app.user_id', true)), false);
$function$;

-- group_for is the group a slug names when the caller may act on it as
-- need asks: 'see' for anyone, 'member' for its members, 'maintainer' for
-- its maintainers. An administrator may do all three.
CREATE FUNCTION group_for(grp_slug text, need text)
RETURNS text
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT g.group_id FROM groups g
   WHERE g.slug = grp_slug
     AND (need = 'see' OR caller_is_administrator()
          OR EXISTS (SELECT 1 FROM group_members m
                      WHERE m.group_id = g.group_id
                        AND m.user_id = current_setting('app.user_id', true)
                        AND (need = 'member' OR m.role = 'maintainer')));
$function$;

-- maintainers_left is how many maintainers a group keeps if member's role
-- became new_role. A NULL role is the membership taken away.
CREATE FUNCTION maintainers_left(grp text, member text, new_role text)
RETURNS bigint
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT (SELECT count(*) FROM group_members
           WHERE group_id = grp AND role = 'maintainer' AND user_id <> member)
         + CASE WHEN new_role = 'maintainer' THEN 1 ELSE 0 END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION create_group(new_slug text, new_name text)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor  text := NULLIF(current_setting('app.user_id', true), '');
  new_id text := 'grp_' || substr(md5(random()::text || clock_timestamp()::text), 1, 20);
BEGIN
  IF actor IS NULL THEN
    RAISE EXCEPTION 'no user' USING ERRCODE = 'DSG01';
  END IF;
  IF (SELECT count(*) FROM groups WHERE created_by = actor) >= 100 THEN
    RAISE EXCEPTION 'you have created 100 groups, the most one account may' USING ERRCODE = 'DSL06';
  END IF;
  IF EXISTS (SELECT 1 FROM groups WHERE slug = new_slug) THEN
    RAISE EXCEPTION 'a group named % already exists', new_slug USING ERRCODE = 'DSG04';
  END IF;
  INSERT INTO groups (group_id, slug, name, created_by) VALUES (new_id, new_slug, btrim(new_name), actor);
  INSERT INTO group_members (group_id, user_id, role, source) VALUES (new_id, actor, 'maintainer', 'invited');
  INSERT INTO group_events (group_id, actor, action, subject, role)
    VALUES (new_id, actor, 'create', actor, 'maintainer');
  RETURN new_id;
END;
$function$;

CREATE FUNCTION rename_group(grp_slug text, new_name text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor text := current_setting('app.user_id', true);
  grp   text := group_for(grp_slug, 'maintainer');
  old   text;
BEGIN
  IF grp IS NULL THEN
    RAISE EXCEPTION 'no group named % that you maintain', grp_slug USING ERRCODE = 'DSG01';
  END IF;
  IF btrim(coalesce(new_name, '')) = '' THEN
    RAISE EXCEPTION 'a group needs a name' USING ERRCODE = '22023';
  END IF;
  SELECT name INTO old FROM groups WHERE group_id = grp;
  IF old = btrim(new_name) THEN
    RETURN;
  END IF;
  UPDATE groups SET name = btrim(new_name) WHERE group_id = grp;
  INSERT INTO group_events (group_id, actor, action, subject, detail)
    VALUES (grp, actor, 'rename', actor, old);
END;
$function$;

-- delete_group removes a group and its grants. Its libraries stay, and
-- each records the grant it lost.
CREATE FUNCTION delete_group(grp_slug text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor text := current_setting('app.user_id', true);
  grp   text := group_for(grp_slug, 'maintainer');
BEGIN
  IF grp IS NULL THEN
    RAISE EXCEPTION 'no group named % that you maintain', grp_slug USING ERRCODE = 'DSG01';
  END IF;
  INSERT INTO access_events (library_id, actor, action, role, detail)
    SELECT library_id, actor, 'group_revoke', role, grp_slug
      FROM library_group_grants WHERE group_id = grp;
  DELETE FROM groups WHERE group_id = grp;
END;
$function$;

-- invite_to_group invites someone with no place in the group, and changes
-- the role of a member. It answers which it did: 'invited' or 'changed'.
CREATE FUNCTION invite_to_group(grp_slug text, member_username text, new_role text)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor  text := current_setting('app.user_id', true);
  grp    text := group_for(grp_slug, 'maintainer');
  member text;
BEGIN
  IF grp IS NULL THEN
    RAISE EXCEPTION 'no group named % that you maintain', grp_slug USING ERRCODE = 'DSG01';
  END IF;
  SELECT user_id INTO member FROM users WHERE username = member_username;
  IF member IS NULL THEN
    RAISE EXCEPTION 'no account named %', member_username USING ERRCODE = 'DSL02';
  END IF;
  IF new_role IS NULL OR new_role NOT IN ('member', 'maintainer') THEN
    RAISE EXCEPTION 'a group role is member or maintainer' USING ERRCODE = '22023';
  END IF;
  IF EXISTS (SELECT 1 FROM group_members WHERE group_id = grp AND user_id = member) THEN
    IF maintainers_left(grp, member, new_role) = 0 THEN
      RAISE EXCEPTION 'that would leave % without a maintainer', grp_slug USING ERRCODE = 'DSG03';
    END IF;
    -- A role set by hand stays when the provider's list later drops the person.
    UPDATE group_members SET role = new_role, source = 'invited'
     WHERE group_id = grp AND user_id = member;
    INSERT INTO group_events (group_id, actor, action, subject, role)
      VALUES (grp, actor, 'role', member, new_role);
    RETURN 'changed';
  END IF;
  INSERT INTO group_invitations (group_id, user_id, role, invited_by)
    VALUES (grp, member, new_role, actor)
  ON CONFLICT (group_id, user_id) DO UPDATE
    SET role = EXCLUDED.role, invited_by = EXCLUDED.invited_by, invited_at = now();
  INSERT INTO group_events (group_id, actor, action, subject, role)
    VALUES (grp, actor, 'invite', member, new_role);
  RETURN 'invited';
END;
$function$;

-- group_invitation_for is the group a slug names, where the caller has an
-- open invitation.
CREATE FUNCTION group_invitation_for(grp_slug text)
RETURNS text
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT i.group_id FROM group_invitations i JOIN groups g ON g.group_id = i.group_id
   WHERE g.slug = grp_slug AND i.user_id = current_setting('app.user_id', true);
$function$;

CREATE FUNCTION accept_group_invitation(grp_slug text)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor text := current_setting('app.user_id', true);
  grp   text := group_invitation_for(grp_slug);
  offer group_invitations;
BEGIN
  IF grp IS NULL THEN
    RAISE EXCEPTION 'no invitation to a group named %', grp_slug USING ERRCODE = 'DSG05';
  END IF;
  DELETE FROM group_invitations WHERE group_id = grp AND user_id = actor RETURNING * INTO offer;
  INSERT INTO group_members (group_id, user_id, role, source)
    VALUES (grp, actor, offer.role, 'invited')
  ON CONFLICT (group_id, user_id) DO UPDATE SET role = EXCLUDED.role, source = 'invited';
  INSERT INTO group_events (group_id, actor, action, subject, role)
    VALUES (grp, actor, 'accept', actor, offer.role);
  RETURN offer.role;
END;
$function$;

CREATE FUNCTION decline_group_invitation(grp_slug text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor text := current_setting('app.user_id', true);
  grp   text := group_invitation_for(grp_slug);
BEGIN
  IF grp IS NULL THEN
    RAISE EXCEPTION 'no invitation to a group named %', grp_slug USING ERRCODE = 'DSG05';
  END IF;
  DELETE FROM group_invitations WHERE group_id = grp AND user_id = actor;
  INSERT INTO group_events (group_id, actor, action, subject)
    VALUES (grp, actor, 'decline', actor);
END;
$function$;

-- remove_from_group takes someone out of a group or withdraws their
-- invitation. A maintainer may remove anyone, and anyone may remove
-- themselves, which is how a member leaves. A membership the provider
-- group gives is refused: the provider would give it back.
CREATE FUNCTION remove_from_group(grp_slug text, member_username text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor  text := current_setting('app.user_id', true);
  member text;
  grp    text;
  held   group_members;
BEGIN
  SELECT user_id INTO member FROM users WHERE username = member_username;
  IF member IS NOT NULL AND member = actor THEN
    grp := group_for(grp_slug, 'member');
  ELSE
    grp := group_for(grp_slug, 'maintainer');
  END IF;
  IF grp IS NULL THEN
    RAISE EXCEPTION 'no group named % that you maintain', grp_slug USING ERRCODE = 'DSG01';
  END IF;
  IF member IS NULL THEN
    RAISE EXCEPTION 'no account named %', member_username USING ERRCODE = 'DSL02';
  END IF;
  DELETE FROM group_invitations WHERE group_id = grp AND user_id = member;
  IF FOUND THEN
    INSERT INTO group_events (group_id, actor, action, subject)
      VALUES (grp, actor, 'withdraw', member);
    RETURN;
  END IF;
  SELECT * INTO held FROM group_members WHERE group_id = grp AND user_id = member;
  IF held.user_id IS NULL THEN
    RETURN;
  END IF;
  IF held.source = 'provider' THEN
    RAISE EXCEPTION '% is in % through the provider group', member_username, grp_slug
      USING ERRCODE = 'DSG07';
  END IF;
  IF maintainers_left(grp, member, NULL) = 0 THEN
    RAISE EXCEPTION 'that would leave % without a maintainer', grp_slug USING ERRCODE = 'DSG03';
  END IF;
  DELETE FROM group_members WHERE group_id = grp AND user_id = member;
  INSERT INTO group_events (group_id, actor, action, subject)
    VALUES (grp, actor, 'remove', member);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- all_groups names every group to anyone signed in, with the caller's own
-- role in each, or '' where they hold none.
CREATE FUNCTION all_groups()
RETURNS TABLE (slug text, name text, members bigint, role text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT g.slug, g.name,
         (SELECT count(*) FROM group_members m WHERE m.group_id = g.group_id),
         COALESCE((SELECT m.role FROM group_members m
                    WHERE m.group_id = g.group_id
                      AND m.user_id = current_setting('app.user_id', true)), '')
    FROM groups g
   WHERE NULLIF(current_setting('app.user_id', true), '') IS NOT NULL
   ORDER BY g.slug;
$function$;

CREATE FUNCTION my_group_invitations()
RETURNS TABLE (slug text, name text, role text, invited_by text, invited_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT g.slug, g.name, i.role, COALESCE(u.username, ''), i.invited_at
    FROM group_invitations i
    JOIN groups g ON g.group_id = i.group_id
    LEFT JOIN users u ON u.user_id = i.invited_by
   WHERE i.user_id = current_setting('app.user_id', true)
   ORDER BY i.invited_at DESC, g.slug;
$function$;

-- group_members_of lists a group's members to its members and to
-- administrators. Its maintainers also see who is invited.
CREATE FUNCTION group_members_of(grp_slug text)
RETURNS TABLE (username text, role text, source text, invited boolean)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  grp text := group_for(grp_slug, 'member');
BEGIN
  IF grp IS NULL THEN
    RAISE EXCEPTION 'no group named % that you belong to', grp_slug USING ERRCODE = 'DSG01';
  END IF;
  RETURN QUERY
    SELECT x.who, x.held, x.came, x.pending
      FROM (SELECT COALESCE(u.username, ''), m.role, m.source, false
              FROM group_members m JOIN users u ON u.user_id = m.user_id
             WHERE m.group_id = grp
            UNION ALL
            SELECT COALESCE(u.username, ''), i.role, 'invited', true
              FROM group_invitations i JOIN users u ON u.user_id = i.user_id
             WHERE i.group_id = grp AND group_for(grp_slug, 'maintainer') IS NOT NULL
           ) AS x(who, held, came, pending)
     ORDER BY x.pending, (x.held = 'maintainer') DESC, x.who;
END;
$function$;

-- group_libraries are the libraries granted to a group, for its members.
CREATE FUNCTION group_libraries(grp_slug text)
RETURNS TABLE (slug text, name text, role text)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  grp text := group_for(grp_slug, 'member');
BEGIN
  IF grp IS NULL THEN
    RAISE EXCEPTION 'no group named % that you belong to', grp_slug USING ERRCODE = 'DSG01';
  END IF;
  RETURN QUERY
    SELECT l.slug, l.name, lg.role
      FROM library_group_grants lg JOIN libraries l ON l.library_id = lg.library_id
     WHERE lg.group_id = grp AND l.deleted_at IS NULL
     ORDER BY l.slug;
END;
$function$;

CREATE FUNCTION group_events_of(grp_slug text)
RETURNS TABLE (at timestamptz, actor text, action text, subject text, role text, detail text)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  grp text := group_for(grp_slug, 'maintainer');
BEGIN
  IF grp IS NULL THEN
    RAISE EXCEPTION 'no group named % that you maintain', grp_slug USING ERRCODE = 'DSG01';
  END IF;
  RETURN QUERY
    SELECT e.at, COALESCE(a.username, e.actor_name, ''), e.action,
           COALESCE(s.username, e.subject_name, ''), COALESCE(e.role, ''), COALESCE(e.detail, '')
      FROM group_events e
      LEFT JOIN users a ON a.user_id = e.actor
      LEFT JOIN users s ON s.user_id = e.subject
     WHERE e.group_id = grp
     ORDER BY e.at, e.id;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- grant_library_group gives a group a role on a library, or changes it.
-- The caller administers the library, and maintains the group or is an
-- administrator.
CREATE FUNCTION grant_library_group(lib_slug text, grp_slug text, new_role text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor text := current_setting('app.user_id', true);
  lib   text := library_by_slug(lib_slug, 'admin');
  grp   text := group_for(grp_slug, 'maintainer');
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  IF grp IS NULL THEN
    RAISE EXCEPTION 'no group named % that you maintain', grp_slug USING ERRCODE = 'DSG01';
  END IF;
  IF role_rank(new_role) IS NULL THEN
    RAISE EXCEPTION 'a role is reader, editor or admin' USING ERRCODE = '22023';
  END IF;
  INSERT INTO library_group_grants (library_id, group_id, role, granted_by)
    VALUES (lib, grp, new_role, actor)
  ON CONFLICT (library_id, group_id) DO UPDATE
    SET role = EXCLUDED.role, granted_by = EXCLUDED.granted_by, granted_at = now();
  INSERT INTO access_events (library_id, actor, action, role, detail)
    VALUES (lib, actor, 'group_grant', new_role, grp_slug);
END;
$function$;

-- revoke_library_group takes a group's role on a library away. The
-- library's admins may, and so may the group's maintainers.
CREATE FUNCTION revoke_library_group(lib_slug text, grp_slug text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor text := current_setting('app.user_id', true);
  lib   text := library_by_slug(lib_slug, 'admin');
  grp   text;
BEGIN
  IF lib IS NULL THEN
    grp := group_for(grp_slug, 'maintainer');
    SELECT l.library_id INTO lib
      FROM libraries l JOIN library_group_grants lg ON lg.library_id = l.library_id
     WHERE l.slug = lib_slug AND lg.group_id = grp;
  ELSE
    grp := group_for(grp_slug, 'see');
  END IF;
  IF lib IS NULL OR grp IS NULL THEN
    RAISE EXCEPTION 'no library named % granted to a group you may change', lib_slug
      USING ERRCODE = 'DSL01';
  END IF;
  DELETE FROM library_group_grants WHERE library_id = lib AND group_id = grp;
  IF FOUND THEN
    INSERT INTO access_events (library_id, actor, action, detail)
      VALUES (lib, actor, 'group_revoke', grp_slug);
  END IF;
END;
$function$;

-- library_groups are the groups a library is granted to, for anyone who
-- reads it.
CREATE FUNCTION library_groups(lib_slug text)
RETURNS TABLE (slug text, name text, role text, members bigint)
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
    SELECT g.slug, g.name, lg.role, (SELECT count(*) FROM group_members m WHERE m.group_id = g.group_id)
      FROM library_group_grants lg JOIN groups g ON g.group_id = lg.group_id
     WHERE lg.library_id = lib
     ORDER BY role_rank(lg.role) DESC, g.slug;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- Removing a password account also takes its memberships and invitations,
-- and hands a group it alone maintained to the heir its libraries go to.
CREATE OR REPLACE FUNCTION password_account_removed()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor    text := NULLIF(current_setting('app.user_id', true), '');
  heir     text;
  stranded text[];
  orphans  text[];
BEGIN
  SELECT array_agg(g.library_id ORDER BY l.name) INTO stranded
    FROM library_grants g JOIN libraries l ON l.library_id = g.library_id
   WHERE g.user_id = OLD.user_id AND g.role = 'admin' AND l.kind = 'shared'
     AND NOT EXISTS (SELECT 1 FROM library_grants other
                      WHERE other.library_id = g.library_id AND other.role = 'admin'
                        AND other.user_id <> OLD.user_id);
  SELECT array_agg(m.group_id) INTO orphans
    FROM group_members m
   WHERE m.user_id = OLD.user_id AND m.role = 'maintainer'
     AND NOT EXISTS (SELECT 1 FROM group_members other
                      WHERE other.group_id = m.group_id AND other.role = 'maintainer'
                        AND other.user_id <> OLD.user_id);
  IF stranded IS NOT NULL OR orphans IS NOT NULL THEN
    SELECT user_id INTO heir FROM users
     WHERE username = NULLIF(current_setting('app.heir', true), '')
       AND admin AND user_id <> OLD.user_id;
    IF heir IS NULL THEN
      SELECT user_id INTO heir FROM users
       WHERE admin AND user_id <> OLD.user_id AND username IS NOT NULL
       ORDER BY created_at, user_id LIMIT 1;
    END IF;
  END IF;
  IF stranded IS NOT NULL THEN
    IF heir IS NULL THEN
      RAISE EXCEPTION 'removing % would leave % without an admin, and no other administrator can take over: make someone an administrator first',
        OLD.username,
        (SELECT string_agg(name, ', ' ORDER BY name) FROM libraries WHERE library_id = ANY (stranded))
        USING ERRCODE = 'DSA04';
    END IF;
    DELETE FROM library_invitations WHERE user_id = heir AND library_id = ANY (stranded);
    INSERT INTO library_grants (library_id, user_id, role, granted_by)
      SELECT unnest(stranded), heir, 'admin', actor
    ON CONFLICT (library_id, user_id) DO UPDATE
      SET role = 'admin', granted_by = EXCLUDED.granted_by, granted_at = now();
    INSERT INTO access_events (library_id, actor, action, subject, role)
      SELECT unnest(stranded), actor, 'grant', heir, 'admin';
  END IF;
  -- A group with no maintainer left is managed by the administrators.
  IF orphans IS NOT NULL AND heir IS NOT NULL THEN
    DELETE FROM group_invitations WHERE user_id = heir AND group_id = ANY (orphans);
    INSERT INTO group_members (group_id, user_id, role, source)
      SELECT unnest(orphans), heir, 'maintainer', 'invited'
    ON CONFLICT (group_id, user_id) DO UPDATE SET role = 'maintainer', source = 'invited';
    INSERT INTO group_events (group_id, actor, action, subject, role)
      SELECT unnest(orphans), actor, 'role', heir, 'maintainer';
  END IF;
  INSERT INTO access_events (library_id, actor, action, subject)
    SELECT g.library_id, actor, 'revoke', OLD.user_id
      FROM library_grants g JOIN libraries l ON l.library_id = g.library_id
     WHERE g.user_id = OLD.user_id AND l.kind = 'shared';
  DELETE FROM library_grants g USING libraries l
   WHERE l.library_id = g.library_id AND g.user_id = OLD.user_id AND l.kind = 'shared';
  INSERT INTO access_events (library_id, actor, action, subject)
    SELECT i.library_id, actor, 'withdraw', OLD.user_id
      FROM library_invitations i WHERE i.user_id = OLD.user_id;
  DELETE FROM library_invitations WHERE user_id = OLD.user_id;
  INSERT INTO group_events (group_id, actor, action, subject)
    SELECT m.group_id, actor, 'remove', OLD.user_id FROM group_members m WHERE m.user_id = OLD.user_id;
  DELETE FROM group_members WHERE user_id = OLD.user_id;
  INSERT INTO group_events (group_id, actor, action, subject)
    SELECT i.group_id, actor, 'withdraw', OLD.user_id FROM group_invitations i WHERE i.user_id = OLD.user_id;
  DELETE FROM group_invitations WHERE user_id = OLD.user_id;
  UPDATE users SET username = NULL, admin = false WHERE user_id = OLD.user_id;
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
    'create_group(text, text)', 'rename_group(text, text)', 'delete_group(text)',
    'invite_to_group(text, text, text)', 'accept_group_invitation(text)',
    'decline_group_invitation(text)', 'remove_from_group(text, text)', 'all_groups()',
    'my_group_invitations()', 'group_members_of(text)', 'group_libraries(text)',
    'group_events_of(text)', 'grant_library_group(text, text, text)',
    'revoke_library_group(text, text)', 'library_groups(text)'
  ] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', fn);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO docsearch_app', fn);
  END LOOP;
  FOREACH fn IN ARRAY ARRAY[
    'user_library_roles(text)', 'caller_is_administrator()', 'group_for(text, text)',
    'maintainers_left(text, text, text)', 'group_invitation_for(text)'
  ] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', fn);
  END LOOP;
END;
$grants$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION password_account_removed()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor    text := NULLIF(current_setting('app.user_id', true), '');
  heir     text;
  stranded text[];
BEGIN
  SELECT array_agg(g.library_id ORDER BY l.name) INTO stranded
    FROM library_grants g JOIN libraries l ON l.library_id = g.library_id
   WHERE g.user_id = OLD.user_id AND g.role = 'admin' AND l.kind = 'shared'
     AND NOT EXISTS (SELECT 1 FROM library_grants other
                      WHERE other.library_id = g.library_id AND other.role = 'admin'
                        AND other.user_id <> OLD.user_id);
  IF stranded IS NOT NULL THEN
    SELECT user_id INTO heir FROM users
     WHERE username = NULLIF(current_setting('app.heir', true), '')
       AND admin AND user_id <> OLD.user_id;
    IF heir IS NULL THEN
      SELECT user_id INTO heir FROM users
       WHERE admin AND user_id <> OLD.user_id AND username IS NOT NULL
       ORDER BY created_at, user_id LIMIT 1;
    END IF;
    IF heir IS NULL THEN
      RAISE EXCEPTION 'removing % would leave % without an admin, and no other administrator can take over: make someone an administrator first',
        OLD.username,
        (SELECT string_agg(name, ', ' ORDER BY name) FROM libraries WHERE library_id = ANY (stranded))
        USING ERRCODE = 'DSA04';
    END IF;
    DELETE FROM library_invitations WHERE user_id = heir AND library_id = ANY (stranded);
    INSERT INTO library_grants (library_id, user_id, role, granted_by)
      SELECT unnest(stranded), heir, 'admin', actor
    ON CONFLICT (library_id, user_id) DO UPDATE
      SET role = 'admin', granted_by = EXCLUDED.granted_by, granted_at = now();
    INSERT INTO access_events (library_id, actor, action, subject, role)
      SELECT unnest(stranded), actor, 'grant', heir, 'admin';
  END IF;
  INSERT INTO access_events (library_id, actor, action, subject)
    SELECT g.library_id, actor, 'revoke', OLD.user_id
      FROM library_grants g JOIN libraries l ON l.library_id = g.library_id
     WHERE g.user_id = OLD.user_id AND l.kind = 'shared';
  DELETE FROM library_grants g USING libraries l
   WHERE l.library_id = g.library_id AND g.user_id = OLD.user_id AND l.kind = 'shared';
  INSERT INTO access_events (library_id, actor, action, subject)
    SELECT i.library_id, actor, 'withdraw', OLD.user_id
      FROM library_invitations i WHERE i.user_id = OLD.user_id;
  DELETE FROM library_invitations WHERE user_id = OLD.user_id;
  UPDATE users SET username = NULL, admin = false WHERE user_id = OLD.user_id;
  RETURN OLD;
END;
$function$;

DROP FUNCTION library_groups(text);
DROP FUNCTION revoke_library_group(text, text);
DROP FUNCTION grant_library_group(text, text, text);
DROP FUNCTION group_events_of(text);
DROP FUNCTION group_libraries(text);
DROP FUNCTION group_members_of(text);
DROP FUNCTION my_group_invitations();
DROP FUNCTION all_groups();
DROP FUNCTION remove_from_group(text, text);
DROP FUNCTION decline_group_invitation(text);
DROP FUNCTION accept_group_invitation(text);
DROP FUNCTION group_invitation_for(text);
DROP FUNCTION invite_to_group(text, text, text);
DROP FUNCTION delete_group(text);
DROP FUNCTION rename_group(text, text);
DROP FUNCTION create_group(text, text);
DROP FUNCTION maintainers_left(text, text, text);
DROP FUNCTION group_for(text, text);
DROP FUNCTION caller_is_administrator();

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

DROP FUNCTION user_library_roles(text);

DELETE FROM access_events WHERE action IN ('group_grant', 'group_revoke');
ALTER TABLE access_events DROP CONSTRAINT access_events_action_check;
ALTER TABLE access_events ADD CONSTRAINT access_events_action_check
  CHECK (action IN ('create', 'grant', 'revoke', 'invite', 'accept', 'decline', 'withdraw',
                    'rename', 'delete', 'restore'));

DROP TABLE group_events;
DROP TABLE library_group_grants;
DROP TABLE group_invitations;
DROP TABLE group_members;
DROP TABLE groups;
-- +goose StatementEnd
