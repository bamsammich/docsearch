-- Version 29: removing an account always succeeds, and hands its libraries on.
--
-- Removing a password account that was the only admin of a shared library
-- was refused, naming the library, until someone else was made its admin.
-- That let an account nobody wanted keep itself in place: make a library,
-- and be the only admin of it.
--
-- Now the removal goes ahead. Each shared library the account alone
-- administered is given to an heir as admin, and the grant is recorded in
-- access_events. The heir is the administrator the transaction names in
-- app.heir, which the library UI sets to whoever is removing the account,
-- and the CLI to the administrator it was told to name. When none is named,
-- or the one named is not an administrator, the heir is the longest-standing
-- other administrator. Only when docsearch has no other administrator is the
-- removal refused, still with DSA04.
--
-- A removed account also stops being an administrator. Its users row stays,
-- and an admin flag left on it made a nameless account look like one.
--
-- +goose Up
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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
