-- Version 24: a docsearch administrator manages accounts.
--
-- Until now whoever held the owner DSN managed accounts, through the CLI,
-- and docsearch itself had no administrator. An administrator lists every
-- account, adds and removes password accounts, resets and unlocks them, and
-- makes others administrators, all from the library UI. The role covers
-- accounts and nothing else: an administrator reads a library only through a
-- grant on it, like anyone.
--
-- The request role reads users but cannot write them, so the two changes an
-- administrator makes there go through definer functions. Each acts for the
-- user the transaction named, refuses anyone who is not an administrator, and
-- records what it changed in account_events. The owner DSN stays the root of
-- trust: `docsearch account admin` makes the first administrator.
--
-- +goose Up
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN admin boolean NOT NULL DEFAULT false;

CREATE TABLE account_events (
  id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  at      timestamptz NOT NULL DEFAULT now(),
  actor   text REFERENCES users(user_id) ON DELETE SET NULL,
  action  text NOT NULL CHECK (action IN ('add', 'reset', 'unlock', 'remove', 'admin', 'unadmin')),
  subject text REFERENCES users(user_id) ON DELETE SET NULL
);
CREATE INDEX account_events_at ON account_events(at);
-- +goose StatementEnd

-- +goose StatementBegin
-- set_admin makes an account an administrator, or stops it being one. The
-- last administrator stays one, so the UI always has someone who can manage
-- accounts.
CREATE FUNCTION set_admin(member_username text, make boolean)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  member text;
BEGIN
  IF NOT COALESCE((SELECT admin FROM users WHERE user_id = current_setting('app.user_id', true)), false) THEN
    RAISE EXCEPTION 'only an administrator changes who administers' USING ERRCODE = 'DSA01';
  END IF;
  SELECT user_id INTO member FROM users WHERE username = member_username;
  IF member IS NULL THEN
    RAISE EXCEPTION 'no account named %', member_username USING ERRCODE = 'DSA02';
  END IF;
  IF NOT make AND (SELECT count(*) FROM users WHERE admin AND user_id <> member) = 0 THEN
    RAISE EXCEPTION 'that would leave nobody to administer docsearch' USING ERRCODE = 'DSA03';
  END IF;
  UPDATE users SET admin = make WHERE user_id = member AND admin IS DISTINCT FROM make;
  IF FOUND THEN
    INSERT INTO account_events (actor, action, subject)
      VALUES (current_setting('app.user_id', true), CASE WHEN make THEN 'admin' ELSE 'unadmin' END, member);
  END IF;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- record_account_event logs a change an administrator made to a password
-- account, which the request role writes itself because the hash is made in
-- Go. Anyone else is refused, so the log cannot be forged.
CREATE FUNCTION record_account_event(event_action text, member_username text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
BEGIN
  IF NOT COALESCE((SELECT admin FROM users WHERE user_id = current_setting('app.user_id', true)), false) THEN
    RAISE EXCEPTION 'only an administrator manages accounts' USING ERRCODE = 'DSA01';
  END IF;
  INSERT INTO account_events (actor, action, subject)
    VALUES (current_setting('app.user_id', true), event_action,
            (SELECT user_id FROM users WHERE username = member_username));
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $grants$
DECLARE
  fn text;
BEGIN
  FOREACH fn IN ARRAY ARRAY['set_admin(text, boolean)', 'record_account_event(text, text)'] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', fn);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO docsearch_app', fn);
  END LOOP;
END;
$grants$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION record_account_event(text, text);
DROP FUNCTION set_admin(text, boolean);
DROP TABLE account_events;
ALTER TABLE users DROP COLUMN admin;
-- +goose StatementEnd
