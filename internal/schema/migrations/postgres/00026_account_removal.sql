-- Version 26: removing a password account takes its username and its place
-- in shared libraries with it.
--
-- Removing an account deleted its sign-in and nothing else, so the name it
-- held stayed on its users row. Adding someone under that name again then
-- failed, because a username is unique. Its roles in shared libraries
-- stayed too, so it went on counting as a member, and as an admin, while
-- nobody could sign in as it.
--
-- Now a trigger runs as the sign-in is deleted, from the CLI or from the
-- library UI alike. It refuses the removal when the account is the only
-- admin of a shared library, naming those libraries, so none is left with
-- nobody to manage it. Otherwise it takes the account out of every shared
-- library, recording each in access_events, and frees its username. Its
-- personal library stays, as removing an account always promised.
--
-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION password_account_removed()
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
CREATE TRIGGER password_account_removed BEFORE DELETE ON password_accounts
  FOR EACH ROW EXECUTE FUNCTION password_account_removed();

-- An account removed before this version still holds its name.
UPDATE users u SET username = NULL
 WHERE u.issuer = 'docsearch' AND u.username IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM password_accounts p WHERE p.user_id = u.user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER password_account_removed ON password_accounts;
DROP FUNCTION password_account_removed();
-- +goose StatementEnd
