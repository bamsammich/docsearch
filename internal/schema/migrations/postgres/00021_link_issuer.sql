-- Version 21: an upload link records who issued it.
--
-- A link uploads into one library, and the person who opens it is not
-- signed in: the link is the credential. Until now a link's library was its
-- user's own, so redeeming one named both. In a shared library the two
-- differ, and the upload is written as the person who issued the link, so
-- the policies check that person's grant when the file arrives. Someone
-- removed as an editor after issuing a link can no longer upload through it.
--
-- +goose Up
-- +goose StatementBegin
ALTER TABLE upload_links ADD COLUMN issued_by text REFERENCES users(user_id) ON DELETE CASCADE;

-- Every link so far was issued into its user's own library.
CREATE POLICY migrate ON upload_links TO CURRENT_USER USING (true) WITH CHECK (true);
UPDATE upload_links SET issued_by = library_id WHERE library_id IN (SELECT user_id FROM users);
DROP POLICY migrate ON upload_links;
-- +goose StatementEnd

-- +goose StatementBegin
-- redeem_upload_link now answers with the library and the issuer. The return
-- type changes, so the function is dropped and made again.
DROP FUNCTION redeem_upload_link(text);
CREATE FUNCTION redeem_upload_link(link_hash text)
RETURNS TABLE (library_id text, issued_by text)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  UPDATE upload_links SET used_at = now()
   WHERE token_hash = link_hash AND used_at IS NULL AND expires_at > now()
  RETURNING library_id, issued_by;
$function$;
REVOKE ALL ON FUNCTION redeem_upload_link(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION redeem_upload_link(text) TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION redeem_upload_link(text);
CREATE FUNCTION redeem_upload_link(link_hash text)
RETURNS text
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  UPDATE upload_links SET used_at = now()
   WHERE token_hash = link_hash AND used_at IS NULL AND expires_at > now()
  RETURNING library_id;
$function$;
REVOKE ALL ON FUNCTION redeem_upload_link(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION redeem_upload_link(text) TO docsearch_app;
ALTER TABLE upload_links DROP COLUMN issued_by;
-- +goose StatementEnd
