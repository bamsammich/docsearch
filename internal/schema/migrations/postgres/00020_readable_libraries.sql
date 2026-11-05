-- Version 20: a user can list the libraries they read.
--
-- An unscoped search covers every library the user holds a grant on, and an
-- agent names a shared library by its slug, so a request has to learn both.
-- The request role cannot read library_grants, which is what keeps one user
-- from learning another's grants, so the answer comes from a definer function
-- that reads only the grants of the user the transaction named.
--
-- The user's own library comes first. The rest follow by slug, and a library
-- without one, someone else's personal library shared with them, by id.
--
-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION readable_libraries()
RETURNS TABLE (library_id text, kind text, slug text, name text, role text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT l.library_id, l.kind, l.slug, l.name, g.role
    FROM library_grants g
    JOIN libraries l ON l.library_id = g.library_id
   WHERE g.user_id = current_setting('app.user_id', true)
   ORDER BY (l.library_id = g.user_id) DESC, l.slug NULLS LAST, l.library_id;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
REVOKE ALL ON FUNCTION readable_libraries() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION readable_libraries() TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION readable_libraries();
-- +goose StatementEnd
