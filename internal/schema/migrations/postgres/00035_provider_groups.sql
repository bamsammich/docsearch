-- Version 35: a group can follow a provider group, and administrators can
-- come from one.
--
-- In oidc mode an administrator links a group to a group name the
-- provider puts in its tokens. Whoever carries that name is a member, with
-- source 'provider' and no invitation, because the provider vouches for
-- them. Members invited by hand stay whatever the provider says.
--
-- The server reads the groups claim once for each new token and calls
-- sync_provider_groups with the names it carries. The function adds the
-- account to every linked group it carries, and removes it from every
-- linked group it no longer carries, so someone dropped at the provider
-- leaves with their next token. Removing a provider membership by hand is
-- still refused with DSG07. Changing a group's link, or removing it, takes
-- out every provider member at once, and the new link's members arrive
-- with their next tokens.
--
-- When the operator turns auto-creation on, the server also passes the
-- carried names it should create a group for. A group made this way
-- follows its provider group, takes a slug from the name, and has no
-- maintainer, since the provider decides who is in it. Administrators
-- grant it libraries and manage it.
--
-- sync_provider_admin sets an account's administrator role from the
-- provider group the operator named. It may take the role from the last
-- administrator, unlike set_admin, which refuses with DSA03. Once an admin
-- group is set, the provider decides, and an administrator kept against
-- the provider's word would be one the operator cannot remove. The way
-- back from a provider that names nobody is to set the right group in the
-- environment or docsearch.yaml, which outranks the database, and restart.
--
-- Each of the sync functions names only the account the transaction acts
-- for, the rule set_username keeps since version 30.
--
-- New refusal:
--
--   DSG09  another group already follows that provider group
--
-- +goose Up
-- +goose StatementBegin
ALTER TABLE groups ADD CONSTRAINT groups_provider_group_printable
  CHECK (provider_group IS NULL
         OR (char_length(provider_group) BETWEEN 1 AND 255 AND provider_group !~ '[[:cntrl:]]'));

ALTER TABLE group_events DROP CONSTRAINT group_events_action_check;
ALTER TABLE group_events ADD CONSTRAINT group_events_action_check
  CHECK (action IN ('create', 'rename', 'invite', 'accept', 'decline', 'withdraw', 'role',
                    'remove', 'force', 'link', 'unlink', 'provider_join', 'provider_leave'));

ALTER TABLE account_events DROP CONSTRAINT account_events_action_check;
ALTER TABLE account_events ADD CONSTRAINT account_events_action_check
  CHECK (action IN ('add', 'reset', 'unlock', 'remove', 'admin', 'unadmin',
                    'provider_admin', 'provider_unadmin'));
-- +goose StatementEnd

-- +goose StatementBegin
-- names_only_caller refuses a request that names an account other than
-- the one its transaction acts for. The owner role, which the CLI and the
-- tests use, may name anyone.
CREATE FUNCTION names_only_caller(target_user text)
RETURNS void
LANGUAGE plpgsql STABLE
SET search_path = public, pg_temp
AS $function$
BEGIN
  IF session_user <> current_user
     AND target_user IS DISTINCT FROM NULLIF(current_setting('app.user_id', true), '') THEN
    RAISE EXCEPTION 'an account names only itself' USING ERRCODE = '42501';
  END IF;
END;
$function$;

-- provider_slug is the slug a group made for a provider group starts from:
-- the name lower-cased, each run of other characters a hyphen, cut to 36
-- so a numbered suffix still fits in 40.
CREATE FUNCTION provider_slug(provider_name text)
RETURNS text
LANGUAGE sql IMMUTABLE
SET search_path = public, pg_temp
AS $function$
  SELECT COALESCE(NULLIF(btrim(left(btrim(regexp_replace(lower(provider_name), '[^a-z0-9]+', '-', 'g'),
                                          '-'), 36), '-'), ''), 'group');
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- link_group makes a group follow a provider group, or stops it following
-- one when provider_name is empty. Only an administrator may, because the
-- link hands the group's membership to the provider.
CREATE FUNCTION link_group(grp_slug text, provider_name text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  actor  text := current_setting('app.user_id', true);
  grp    groups;
  wanted text := NULLIF(provider_name, '');
  gone   record;
BEGIN
  IF NOT caller_is_administrator() THEN
    RAISE EXCEPTION 'only an administrator links a group to a provider group' USING ERRCODE = 'DSG01';
  END IF;
  SELECT * INTO grp FROM groups WHERE slug = grp_slug FOR UPDATE;
  IF grp.group_id IS NULL THEN
    RAISE EXCEPTION 'no group named %', grp_slug USING ERRCODE = 'DSG01';
  END IF;
  IF wanted IS NOT DISTINCT FROM grp.provider_group THEN
    RETURN;
  END IF;
  IF wanted IS NOT NULL
     AND EXISTS (SELECT 1 FROM groups WHERE provider_group = wanted AND group_id <> grp.group_id) THEN
    RAISE EXCEPTION 'another group already follows %', wanted USING ERRCODE = 'DSG09';
  END IF;
  -- The old link's members came from it, so they go with it.
  FOR gone IN
    DELETE FROM group_members WHERE group_id = grp.group_id AND source = 'provider' RETURNING user_id
  LOOP
    INSERT INTO group_events (group_id, actor, action, subject, detail)
      VALUES (grp.group_id, actor, 'provider_leave', gone.user_id, grp.provider_group);
  END LOOP;
  BEGIN
    UPDATE groups SET provider_group = wanted WHERE group_id = grp.group_id;
  EXCEPTION WHEN unique_violation THEN
    RAISE EXCEPTION 'another group already follows %', wanted USING ERRCODE = 'DSG09';
  END;
  INSERT INTO group_events (group_id, actor, action, detail)
    VALUES (grp.group_id, actor, CASE WHEN wanted IS NULL THEN 'unlink' ELSE 'link' END,
            COALESCE(wanted, grp.provider_group));
END;
$function$;

-- group_provider_link is the provider group a group follows, for an
-- administrator, and '' for a group that follows none. Anyone else gets
-- NULL. Anyone signed in may see a group's name, so a slug no group has is
-- refused alike for everyone.
CREATE FUNCTION group_provider_link(grp_slug text)
RETURNS text
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  grp groups;
BEGIN
  SELECT * INTO grp FROM groups WHERE slug = grp_slug
     AND NULLIF(current_setting('app.user_id', true), '') IS NOT NULL;
  IF grp.group_id IS NULL THEN
    RAISE EXCEPTION 'no group named %', grp_slug USING ERRCODE = 'DSG01';
  END IF;
  IF NOT caller_is_administrator() THEN
    RETURN NULL;
  END IF;
  RETURN COALESCE(grp.provider_group, '');
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- sync_provider_groups makes target_user's provider memberships match the
-- provider group names its token carries, and creates a linked group for
-- each name in create_missing that no group follows yet. It answers how
-- many memberships it changed.
CREATE FUNCTION sync_provider_groups(target_user text, carried text[], create_missing text[])
RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  pname   text;
  pslug   text;
  n       integer;
  changed integer := 0;
  moved   record;
BEGIN
  PERFORM names_only_caller(target_user);
  carried := COALESCE(carried, '{}');
  FOREACH pname IN ARRAY COALESCE(create_missing, '{}') LOOP
    CONTINUE WHEN NOT pname = ANY(carried)
               OR char_length(pname) NOT BETWEEN 1 AND 255
               OR pname ~ '[[:cntrl:]]'
               OR NOT group_name_ok(left(pname, 120))
               OR EXISTS (SELECT 1 FROM groups g WHERE g.provider_group = pname);
    pslug := provider_slug(pname);
    n := 1;
    WHILE EXISTS (SELECT 1 FROM groups g WHERE g.slug = pslug) LOOP
      n := n + 1;
      pslug := provider_slug(pname) || '-' || n;
    END LOOP;
    BEGIN
      WITH made AS (
        INSERT INTO groups (group_id, slug, name, provider_group)
          VALUES ('grp_' || substr(md5(random()::text || clock_timestamp()::text), 1, 20),
                  pslug, btrim(left(pname, 120)), pname)
        RETURNING group_id
      )
      INSERT INTO group_events (group_id, action, detail) SELECT group_id, 'create', pname FROM made;
    EXCEPTION WHEN unique_violation THEN
      -- Another sign-in made it first, which is the group wanted.
      NULL;
    END;
  END LOOP;

  FOR moved IN
    DELETE FROM group_members m USING groups g
     WHERE m.group_id = g.group_id AND m.user_id = target_user AND m.source = 'provider'
       AND (g.provider_group IS NULL OR NOT g.provider_group = ANY(carried))
    RETURNING g.group_id, g.provider_group
  LOOP
    INSERT INTO group_events (group_id, action, subject, detail)
      VALUES (moved.group_id, 'provider_leave', target_user, moved.provider_group);
    changed := changed + 1;
  END LOOP;

  FOR moved IN
    INSERT INTO group_members (group_id, user_id, role, source)
      SELECT g.group_id, target_user, 'member', 'provider' FROM groups g
       WHERE g.provider_group = ANY(carried)
    ON CONFLICT (group_id, user_id) DO NOTHING
    RETURNING group_id
  LOOP
    DELETE FROM group_invitations WHERE group_id = moved.group_id AND user_id = target_user;
    INSERT INTO group_events (group_id, action, subject, detail)
      VALUES (moved.group_id, 'provider_join', target_user,
              (SELECT g.provider_group FROM groups g WHERE g.group_id = moved.group_id));
    changed := changed + 1;
  END LOOP;
  RETURN changed;
END;
$function$;

-- sync_provider_admin sets target_user's administrator role as the
-- provider's admin group says, and answers whether it changed.
CREATE FUNCTION sync_provider_admin(target_user text, make boolean)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
BEGIN
  PERFORM names_only_caller(target_user);
  UPDATE users SET admin = make WHERE user_id = target_user AND admin IS DISTINCT FROM make;
  IF NOT FOUND THEN
    RETURN false;
  END IF;
  INSERT INTO account_events (action, subject)
    VALUES (CASE WHEN make THEN 'provider_admin' ELSE 'provider_unadmin' END, target_user);
  RETURN true;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $grants$
DECLARE
  fn text;
BEGIN
  FOREACH fn IN ARRAY ARRAY[
    'link_group(text, text)', 'group_provider_link(text)',
    'sync_provider_groups(text, text[], text[])', 'sync_provider_admin(text, boolean)'
  ] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', fn);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO docsearch_app', fn);
  END LOOP;
  FOREACH fn IN ARRAY ARRAY['names_only_caller(text)', 'provider_slug(text)'] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', fn);
  END LOOP;
END;
$grants$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION sync_provider_admin(text, boolean);
DROP FUNCTION sync_provider_groups(text, text[], text[]);
DROP FUNCTION group_provider_link(text);
DROP FUNCTION link_group(text, text);
DROP FUNCTION provider_slug(text);
DROP FUNCTION names_only_caller(text);

DELETE FROM account_events WHERE action IN ('provider_admin', 'provider_unadmin');
ALTER TABLE account_events DROP CONSTRAINT account_events_action_check;
ALTER TABLE account_events ADD CONSTRAINT account_events_action_check
  CHECK (action IN ('add', 'reset', 'unlock', 'remove', 'admin', 'unadmin'));

DELETE FROM group_events WHERE action IN ('link', 'unlink', 'provider_join', 'provider_leave');
ALTER TABLE group_events DROP CONSTRAINT group_events_action_check;
ALTER TABLE group_events ADD CONSTRAINT group_events_action_check
  CHECK (action IN ('create', 'rename', 'invite', 'accept', 'decline', 'withdraw', 'role',
                    'remove', 'force'));

ALTER TABLE groups DROP CONSTRAINT groups_provider_group_printable;
-- +goose StatementEnd
