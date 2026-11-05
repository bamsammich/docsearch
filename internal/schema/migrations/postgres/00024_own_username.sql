-- Version 24: an account reads its own username.
--
-- The request role holds no read on users. The library UI needs the one
-- username it may see, its own, to mark the signed-in person in a member
-- list and to leave a library, which revoke_library takes a username for.
--
-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION own_username()
RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT COALESCE(username, '') FROM users WHERE user_id = current_setting('app.user_id', true);
$function$;
REVOKE ALL ON FUNCTION own_username() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION own_username() TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION own_username();
-- +goose StatementEnd
