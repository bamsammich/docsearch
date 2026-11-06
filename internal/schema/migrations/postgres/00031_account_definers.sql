-- Version 31: only an administrator, or the owner role, adds, resets and
-- removes password accounts.
--
-- The request role could insert, update and delete password_accounts
-- directly, so the rule that only an administrator manages accounts lived
-- in Go alone. Anything that reached the database as the request role could
-- add itself an account, or remove someone else's.
--
-- Now the three changes go through definer functions, the way every other
-- library and account rule does. Each refuses with DSA01 unless the
-- transaction's app.user_id is a docsearch administrator, or the session is
-- the owner role, which `docsearch account` connects as. The request role
-- keeps reading the table, and keeps updating the three columns signing in
-- writes: the failure count, the lock, and the hash a correct password is
-- rehashed into when its costs are out of date.
--
-- +goose Up
-- +goose StatementBegin
-- administers_accounts is whether the caller may change password accounts.
-- Inside a definer current_user is the owner, so session_user matching it is
-- a connection made as the owner role.
CREATE FUNCTION administers_accounts()
RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT session_user = current_user
      OR COALESCE((SELECT admin FROM users
                    WHERE user_id = NULLIF(current_setting('app.user_id', true), '')), false);
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- add_password_account gives an existing user a password sign-in. The
-- user's library is made first, in the same transaction, by create_account
-- or claim_builtin.
CREATE FUNCTION add_password_account(new_username text, new_user_id text, new_hash text)
RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
BEGIN
  IF NOT administers_accounts() THEN
    RAISE EXCEPTION 'only an administrator manages accounts' USING ERRCODE = 'DSA01';
  END IF;
  INSERT INTO password_accounts (username, user_id, password_hash)
    VALUES (new_username, new_user_id, new_hash);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- reset_password replaces an account's password, lifts any lock, and ends
-- every sign-in the account holds. It answers whether the account exists.
CREATE FUNCTION reset_password(member_username text, new_hash text)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  member text;
BEGIN
  IF NOT administers_accounts() THEN
    RAISE EXCEPTION 'only an administrator manages accounts' USING ERRCODE = 'DSA01';
  END IF;
  UPDATE password_accounts
     SET password_hash = new_hash, failed_attempts = 0, locked_until = NULL
   WHERE username = member_username
  RETURNING user_id INTO member;
  IF member IS NULL THEN
    RETURN false;
  END IF;
  DELETE FROM oauth_refresh_tokens WHERE user_id = member;
  RETURN true;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- remove_password_account ends an account's sign-in and every sign-in it
-- holds. The password_account_removed trigger hands its shared libraries to
-- the heir app.heir names and records app.user_id as the one who removed it,
-- as migration 29 describes. It answers whether the account existed.
CREATE FUNCTION remove_password_account(member_username text)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  member text;
BEGIN
  IF NOT administers_accounts() THEN
    RAISE EXCEPTION 'only an administrator manages accounts' USING ERRCODE = 'DSA01';
  END IF;
  DELETE FROM password_accounts WHERE username = member_username
  RETURNING user_id INTO member;
  IF member IS NULL THEN
    RETURN false;
  END IF;
  DELETE FROM oauth_refresh_tokens WHERE user_id = member;
  RETURN true;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $grants$
DECLARE
  fn text;
BEGIN
  REVOKE ALL ON FUNCTION administers_accounts() FROM PUBLIC;
  FOREACH fn IN ARRAY ARRAY[
    'add_password_account(text, text, text)',
    'reset_password(text, text)',
    'remove_password_account(text)'
  ] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', fn);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO docsearch_app', fn);
  END LOOP;
END;
$grants$;

-- Revoking UPDATE on the table also revokes it on every column, so the
-- column grant comes after.
REVOKE INSERT, UPDATE, DELETE ON password_accounts FROM docsearch_app;
GRANT UPDATE (failed_attempts, locked_until, password_hash) ON password_accounts TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
REVOKE UPDATE ON password_accounts FROM docsearch_app;
GRANT INSERT, UPDATE, DELETE ON password_accounts TO docsearch_app;
DROP FUNCTION remove_password_account(text);
DROP FUNCTION reset_password(text, text);
DROP FUNCTION add_password_account(text, text, text);
DROP FUNCTION administers_accounts();
-- +goose StatementEnd
