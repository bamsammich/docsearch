-- Version 11: an identity can claim the built-in library.
--
-- A deployment that ran without accounts has one library, owned by the
-- built-in user, whose row carries no identity. Switching to oidc hands that
-- library to the identity the operator names, rather than starting that
-- person an empty library beside the one they already filled.
--
-- The request role holds only SELECT on users, so the claim goes through a
-- SECURITY DEFINER function for the same reason create_account does. It
-- updates the one row with no identity and nothing else: NULLS NOT DISTINCT
-- on users_identity allows exactly one such row, and once it is claimed the
-- function finds nothing to update, so a second identity cannot take the
-- library from the first.
--
-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION claim_builtin(new_issuer text, new_subject text)
RETURNS text
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  UPDATE users SET issuer = new_issuer, subject = new_subject
   WHERE issuer IS NULL
  RETURNING user_id;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
REVOKE ALL ON FUNCTION claim_builtin(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_builtin(text, text) TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION claim_builtin(text, text);
-- +goose StatementEnd
