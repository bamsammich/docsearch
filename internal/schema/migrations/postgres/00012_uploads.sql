-- Version 12: uploaded files, and the signed links a browser uploads through.
--
-- An agent cannot carry a file in a tool call, so a file reaches docsearch by
-- a side door: a short-lived link a person opens, or an authenticated PUT.
-- The bytes are kept here rather than on a disk, because the server that
-- receives them and the worker that reads them are separate processes, often
-- on separate hosts, and the database is the one thing both already share.
-- Keeping them also lets a refresh read the file again.
--
-- uploads is a tenant table like every other: row-level security by user,
-- and the request role may only see its own. key is what an upload is
-- replaced by. A client that names one, as a sync does, gets its document
-- replaced in place on the next upload; one that does not gets the content
-- hash, so the same bytes uploaded twice are one document.
--
-- upload_links holds a hash of each token, never the token. The link is the
-- credential, since a browser cannot attach a bearer token, so a leaked table
-- must not leak working links. Redeeming one happens before any user is
-- known, so it goes through a definer function that marks the link used in
-- the same statement that reads it: two uploads racing on one link cannot
-- both win.
--
-- +goose Up
-- +goose StatementBegin
CREATE TABLE uploads (
  user_id     TEXT NOT NULL REFERENCES users(user_id),
  key         TEXT NOT NULL,
  filename    TEXT NOT NULL,
  sha256      TEXT NOT NULL,
  content     BYTEA NOT NULL,
  uploaded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, key)
);

CREATE TABLE upload_links (
  token_hash TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(user_id),
  expires_at TIMESTAMPTZ NOT NULL,
  used_at    TIMESTAMPTZ
);

ALTER TABLE uploads      ENABLE ROW LEVEL SECURITY;
ALTER TABLE uploads      FORCE  ROW LEVEL SECURITY;
ALTER TABLE upload_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE upload_links FORCE  ROW LEVEL SECURITY;

CREATE POLICY own ON uploads      USING (user_id = current_setting('app.user_id', true))
  WITH CHECK (user_id = current_setting('app.user_id', true));
CREATE POLICY own ON upload_links USING (user_id = current_setting('app.user_id', true))
  WITH CHECK (user_id = current_setting('app.user_id', true));

-- FORCE binds the owner too, and the two functions below run as the owner
-- before any user is known, so they would see no link at all. This policy
-- lets the role that owns them, the one running this migration, see every
-- link. Requests never run as that role, and the request role keeps only its
-- own rows.
CREATE POLICY redeem ON upload_links TO CURRENT_USER USING (true) WITH CHECK (true);

GRANT SELECT, INSERT, UPDATE, DELETE ON uploads, upload_links TO docsearch_app;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION redeem_upload_link(link_hash text)
RETURNS text
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  UPDATE upload_links SET used_at = now()
   WHERE token_hash = link_hash AND used_at IS NULL AND expires_at > now()
  RETURNING user_id;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- upload_link_open answers whether a link still works, without spending it,
-- so the page a person opens can say so before they choose a file.
CREATE FUNCTION upload_link_open(link_hash text)
RETURNS boolean
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT EXISTS (SELECT 1 FROM upload_links
                  WHERE token_hash = link_hash AND used_at IS NULL AND expires_at > now());
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
REVOKE ALL ON FUNCTION redeem_upload_link(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION upload_link_open(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION redeem_upload_link(text) TO docsearch_app;
GRANT EXECUTE ON FUNCTION upload_link_open(text) TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION upload_link_open(text);
DROP FUNCTION redeem_upload_link(text);
REVOKE ALL ON uploads, upload_links FROM docsearch_app;
DROP POLICY redeem ON upload_links;
DROP POLICY own ON upload_links;
DROP POLICY own ON uploads;
DROP TABLE upload_links;
DROP TABLE uploads;
-- +goose StatementEnd
