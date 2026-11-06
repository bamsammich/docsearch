-- Version 22: an account has a username, so someone can be invited by name.
--
-- An account is an issuer and a subject, and an oidc subject is the
-- provider's opaque identifier, which nobody can type. Sharing a library
-- needs a name a person knows. A password account's username is the one it
-- signs in with. An oidc account's is the token's preferred_username, or its
-- email where the provider sends no username, read when the account signs in.
-- An account that has never signed in has none, and cannot be invited until
-- it does.
--
-- A username is unique across the deployment. Two providers can both call
-- someone alice, and the second keeps no username rather than taking the
-- first's, because an invitation must reach exactly the person it names.
--
-- +goose Up
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN username text UNIQUE
  CHECK (username ~ '^[a-z0-9._@+-]{1,128}$');

UPDATE users SET username = p.username
  FROM password_accounts p WHERE p.user_id = users.user_id;
-- +goose StatementEnd

-- +goose StatementBegin
-- set_username records the name an account signed in under. The request
-- role cannot write users, so it asks through this function. A name another
-- account already holds is left with that account, and the function answers
-- whether the name is now this account's.
CREATE FUNCTION set_username(target_user text, new_username text)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
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
REVOKE ALL ON FUNCTION set_username(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION set_username(text, text) TO docsearch_app;
-- +goose StatementEnd

-- +goose StatementBegin
-- A password account's username is the one docsearch account add gave it.
CREATE FUNCTION password_account_username()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
BEGIN
  UPDATE users SET username = NEW.username WHERE user_id = NEW.user_id;
  RETURN NEW;
END;
$function$;
CREATE TRIGGER password_account_username AFTER INSERT ON password_accounts
  FOR EACH ROW EXECUTE FUNCTION password_account_username();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER password_account_username ON password_accounts;
DROP FUNCTION password_account_username();
DROP FUNCTION set_username(text, text);
ALTER TABLE users DROP COLUMN username;
-- +goose StatementEnd
