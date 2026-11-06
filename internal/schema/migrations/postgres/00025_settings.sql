-- Version 25: a docsearch started without docsearch.yaml keeps the settings
-- an administrator changes in the database.
--
-- A deployment keeps its settings in one place. Started with docsearch.yaml,
-- the file holds them and a change made in the library UI is never stored:
-- it becomes the updated file, for the operator to commit and deploy.
-- Started without one, a change made in the UI is kept here and read when
-- the server next starts. Either way the environment and the command line
-- override what is kept, so an operator can always correct a setting
-- without the UI.
--
-- The request role reads this table. It writes only through set_setting,
-- which refuses anyone who is not an administrator.
--
-- +goose Up
-- +goose StatementBegin
CREATE TABLE settings (
  key        text PRIMARY KEY,
  value      text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by text REFERENCES users(user_id) ON DELETE SET NULL
);
GRANT SELECT ON settings TO docsearch_app;
-- +goose StatementEnd

-- +goose StatementBegin
-- set_setting keeps a setting's value, or forgets it when the value is NULL
-- so the setting falls back to its default.
CREATE FUNCTION set_setting(setting_key text, setting_value text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
BEGIN
  IF NOT COALESCE((SELECT admin FROM users WHERE user_id = current_setting('app.user_id', true)), false) THEN
    RAISE EXCEPTION 'only an administrator changes settings' USING ERRCODE = 'DSA01';
  END IF;
  IF setting_value IS NULL THEN
    DELETE FROM settings WHERE key = setting_key;
    RETURN;
  END IF;
  INSERT INTO settings (key, value, updated_by)
    VALUES (setting_key, setting_value, current_setting('app.user_id', true))
    ON CONFLICT (key) DO UPDATE
      SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by;
END;
$function$;
REVOKE ALL ON FUNCTION set_setting(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION set_setting(text, text) TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION set_setting(text, text);
DROP TABLE settings;
-- +goose StatementEnd
