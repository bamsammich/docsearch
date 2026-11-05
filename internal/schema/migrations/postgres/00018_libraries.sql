-- Version 18: rows belong to a library, and a user reaches a library through
-- a grant.
--
-- Until now the owner of a row was a user, and a user had one library. A
-- library becomes the owner instead, so several people can share one. Every
-- user keeps a personal library whose library_id is the user's own id, which
-- is why the owner column can be renamed rather than rewritten: no row moves,
-- no partition is renamed, and a single-user install sees no difference.
--
-- A policy now asks two things. Which library the transaction works in is
-- app.library_id, a scalar the application sets beside app.user_id. A scalar
-- is what lets Postgres prune chunks to one partition when the executor
-- starts, even in a generic plan; a policy that compares against an array of
-- granted libraries prunes nothing, and every query would visit every
-- library's partition. Whether the user may work there is the grant check,
-- which reads library_grants through a definer function, run once per
-- statement as an InitPlan. The grant check is the guard: an application that
-- names the wrong library gets no rows rather than someone else's.
--
-- Reading needs reader. Writing needs editor. The queue and the crawl cache
-- belong to the people who add documents, so even reading them needs editor.
--
-- +goose Up
-- +goose StatementBegin

CREATE TABLE libraries (
  library_id text PRIMARY KEY CHECK (library_id ~ '^[a-z0-9_-]{1,40}$'),
  kind       text NOT NULL CHECK (kind IN ('personal', 'shared')),
  -- A shared library has a slug agents type and a name people read. A
  -- personal library is addressed as the caller's own and needs neither.
  slug       text UNIQUE CHECK (slug ~ '^[a-z0-9-]{1,40}$'),
  name       text,
  created_by text REFERENCES users(user_id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT libraries_shared_named CHECK ((kind = 'shared') = (slug IS NOT NULL AND name IS NOT NULL))
);

CREATE TABLE library_grants (
  library_id text NOT NULL REFERENCES libraries(library_id) ON DELETE CASCADE,
  user_id    text NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('reader', 'editor', 'admin')),
  granted_by text REFERENCES users(user_id) ON DELETE SET NULL,
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (library_id, user_id)
);
CREATE INDEX library_grants_user ON library_grants(user_id);

-- Every user that exists owns the library its rows already carry.
INSERT INTO libraries (library_id, kind) SELECT user_id, 'personal' FROM users;
INSERT INTO library_grants (library_id, user_id, role)
  SELECT user_id, user_id, 'admin' FROM users;

ALTER TABLE documents    RENAME COLUMN user_id TO library_id;
ALTER TABLE chunks       RENAME COLUMN user_id TO library_id;
ALTER TABLE pages        RENAME COLUMN user_id TO library_id;
ALTER TABLE index_terms  RENAME COLUMN user_id TO library_id;
ALTER TABLE ingest_jobs  RENAME COLUMN user_id TO library_id;
ALTER TABLE uploads      RENAME COLUMN user_id TO library_id;
ALTER TABLE upload_links RENAME COLUMN user_id TO library_id;
ALTER TABLE responses    RENAME COLUMN user_id TO library_id;
ALTER TABLE robots       RENAME COLUMN user_id TO library_id;

-- An owner is a library now, so the keys that named a user name one.
ALTER TABLE documents    DROP CONSTRAINT documents_user_fkey;
ALTER TABLE uploads      DROP CONSTRAINT uploads_user_id_fkey;
ALTER TABLE upload_links DROP CONSTRAINT upload_links_user_id_fkey;
ALTER TABLE responses    DROP CONSTRAINT responses_user_id_fkey;
ALTER TABLE robots       DROP CONSTRAINT robots_user_id_fkey;
ALTER TABLE documents    ADD CONSTRAINT documents_library_fkey
  FOREIGN KEY (library_id) REFERENCES libraries(library_id);
ALTER TABLE uploads      ADD CONSTRAINT uploads_library_fkey
  FOREIGN KEY (library_id) REFERENCES libraries(library_id);
ALTER TABLE upload_links ADD CONSTRAINT upload_links_library_fkey
  FOREIGN KEY (library_id) REFERENCES libraries(library_id);
ALTER TABLE responses    ADD CONSTRAINT responses_library_fkey
  FOREIGN KEY (library_id) REFERENCES libraries(library_id);
ALTER TABLE robots       ADD CONSTRAINT robots_library_fkey
  FOREIGN KEY (library_id) REFERENCES libraries(library_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- The ranks a grant check compares. Immutable, so the planner folds a
-- constant argument once.
CREATE FUNCTION role_rank(role text) RETURNS integer
LANGUAGE sql IMMUTABLE STRICT
AS $function$
  SELECT CASE role WHEN 'admin' THEN 3 WHEN 'editor' THEN 2 WHEN 'reader' THEN 1 END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
-- app_libraries is every library the transaction's user holds at least
-- min_role on. A definer, because the request role cannot read
-- library_grants: a user may learn what they were granted through this
-- function, and nothing about anyone else's grants.
CREATE FUNCTION app_libraries(min_role text) RETURNS text[]
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  SELECT COALESCE(array_agg(library_id), '{}')
    FROM library_grants
   WHERE user_id = current_setting('app.user_id', true)
     AND role_rank(role) >= role_rank(min_role);
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
REVOKE ALL ON FUNCTION app_libraries(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app_libraries(text) TO docsearch_app;

-- create_account now makes the personal library and its grant with the row
-- and the partition, so an account never exists without a library it can use.
CREATE OR REPLACE FUNCTION create_account(new_user_id text, new_issuer text DEFAULT NULL,
                                          new_subject text DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  partition_name text := 'chunks_' || new_user_id;
BEGIN
  INSERT INTO users (user_id, issuer, subject)
    VALUES (new_user_id, new_issuer, new_subject);
  INSERT INTO libraries (library_id, kind, created_by)
    VALUES (new_user_id, 'personal', new_user_id);
  INSERT INTO library_grants (library_id, user_id, role)
    VALUES (new_user_id, new_user_id, 'admin');
  EXECUTE format('CREATE TABLE %I PARTITION OF chunks FOR VALUES IN (%L)',
                 partition_name, new_user_id);
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO docsearch_app',
                 partition_name);
END;
$function$;

-- The column the function returns was renamed under it, and a SQL function's
-- body is kept as text, so it is written again.
CREATE OR REPLACE FUNCTION redeem_upload_link(link_hash text)
RETURNS text
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  UPDATE upload_links SET used_at = now()
   WHERE token_hash = link_hash AND used_at IS NULL AND expires_at > now()
  RETURNING library_id;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $policies$
DECLARE
  -- table, the role reading it needs, the role writing it needs
  spec   text[];
  specs  text[][] := ARRAY[
    ARRAY['documents',    'reader', 'editor'],
    ARRAY['chunks',       'reader', 'editor'],
    ARRAY['pages',        'reader', 'editor'],
    ARRAY['index_terms',  'reader', 'editor'],
    ARRAY['uploads',      'reader', 'editor'],
    ARRAY['ingest_jobs',  'editor', 'editor'],
    ARRAY['upload_links', 'editor', 'editor'],
    ARRAY['responses',    'editor', 'editor'],
    ARRAY['robots',       'editor', 'editor']];
  scoped text := $q$library_id = current_setting('app.library_id', true)$q$;
BEGIN
  FOREACH spec SLICE 1 IN ARRAY specs LOOP
    EXECUTE format('DROP POLICY own ON %I', spec[1]);
    EXECUTE format(
      'CREATE POLICY lib_read ON %I FOR SELECT USING (%s AND library_id = ANY ((SELECT app_libraries(%L))::text[]))',
      spec[1], scoped, spec[2]);
    EXECUTE format(
      'CREATE POLICY lib_insert ON %I FOR INSERT WITH CHECK (%s AND library_id = ANY ((SELECT app_libraries(%L))::text[]))',
      spec[1], scoped, spec[3]);
    EXECUTE format(
      'CREATE POLICY lib_update ON %I FOR UPDATE USING (%s AND library_id = ANY ((SELECT app_libraries(%L))::text[])) '
      'WITH CHECK (%s AND library_id = ANY ((SELECT app_libraries(%L))::text[]))',
      spec[1], scoped, spec[3], scoped, spec[3]);
    EXECUTE format(
      'CREATE POLICY lib_delete ON %I FOR DELETE USING (%s AND library_id = ANY ((SELECT app_libraries(%L))::text[]))',
      spec[1], scoped, spec[3]);
  END LOOP;
END;
$policies$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $policies$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['documents', 'chunks', 'pages', 'index_terms', 'uploads',
                           'ingest_jobs', 'upload_links', 'responses', 'robots'] LOOP
    EXECUTE format('DROP POLICY lib_read ON %I', t);
    EXECUTE format('DROP POLICY lib_insert ON %I', t);
    EXECUTE format('DROP POLICY lib_update ON %I', t);
    EXECUTE format('DROP POLICY lib_delete ON %I', t);
  END LOOP;
END;
$policies$;

-- Going back to one library per user keeps only the libraries a user owns.
-- A shared library's rows and partition go with it, as migration 6's own
-- down step keeps only the built-in user's. FORCE binds the role running
-- this, and with the library policies gone nothing would match, so a policy
-- of its own lets it see the rows it removes.
DO $shared$
DECLARE
  t   text;
  lib text;
  tenant text[] := ARRAY['pages', 'index_terms', 'documents', 'ingest_jobs', 'uploads',
                         'upload_links', 'responses', 'robots'];
BEGIN
  FOREACH t IN ARRAY tenant LOOP
    EXECUTE format('CREATE POLICY migrate ON %I TO CURRENT_USER USING (true)', t);
  END LOOP;
  FOR lib IN SELECT library_id FROM libraries WHERE kind = 'shared' LOOP
    EXECUTE format('DROP TABLE IF EXISTS %I', 'chunks_' || lib);
    FOREACH t IN ARRAY tenant LOOP
      EXECUTE format('DELETE FROM %I WHERE library_id = %L', t, lib);
    END LOOP;
  END LOOP;
  FOREACH t IN ARRAY tenant LOOP
    EXECUTE format('DROP POLICY migrate ON %I', t);
  END LOOP;
END;
$shared$;

ALTER TABLE documents    DROP CONSTRAINT documents_library_fkey;
ALTER TABLE uploads      DROP CONSTRAINT uploads_library_fkey;
ALTER TABLE upload_links DROP CONSTRAINT upload_links_library_fkey;
ALTER TABLE responses    DROP CONSTRAINT responses_library_fkey;
ALTER TABLE robots       DROP CONSTRAINT robots_library_fkey;

ALTER TABLE documents    RENAME COLUMN library_id TO user_id;
ALTER TABLE chunks       RENAME COLUMN library_id TO user_id;
ALTER TABLE pages        RENAME COLUMN library_id TO user_id;
ALTER TABLE index_terms  RENAME COLUMN library_id TO user_id;
ALTER TABLE ingest_jobs  RENAME COLUMN library_id TO user_id;
ALTER TABLE uploads      RENAME COLUMN library_id TO user_id;
ALTER TABLE upload_links RENAME COLUMN library_id TO user_id;
ALTER TABLE responses    RENAME COLUMN library_id TO user_id;
ALTER TABLE robots       RENAME COLUMN library_id TO user_id;

ALTER TABLE documents    ADD CONSTRAINT documents_user_fkey
  FOREIGN KEY (user_id) REFERENCES users(user_id);
ALTER TABLE uploads      ADD CONSTRAINT uploads_user_id_fkey
  FOREIGN KEY (user_id) REFERENCES users(user_id);
ALTER TABLE upload_links ADD CONSTRAINT upload_links_user_id_fkey
  FOREIGN KEY (user_id) REFERENCES users(user_id);
ALTER TABLE responses    ADD CONSTRAINT responses_user_id_fkey
  FOREIGN KEY (user_id) REFERENCES users(user_id);
ALTER TABLE robots       ADD CONSTRAINT robots_user_id_fkey
  FOREIGN KEY (user_id) REFERENCES users(user_id);

DO $policies$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['documents', 'chunks', 'pages', 'index_terms', 'uploads',
                           'ingest_jobs', 'upload_links', 'responses', 'robots'] LOOP
    EXECUTE format(
      'CREATE POLICY own ON %I USING (user_id = current_setting(''app.user_id'', true)) '
      'WITH CHECK (user_id = current_setting(''app.user_id'', true))', t);
  END LOOP;
END;
$policies$;

CREATE OR REPLACE FUNCTION redeem_upload_link(link_hash text)
RETURNS text
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
  UPDATE upload_links SET used_at = now()
   WHERE token_hash = link_hash AND used_at IS NULL AND expires_at > now()
  RETURNING user_id;
$function$;

CREATE OR REPLACE FUNCTION create_account(new_user_id text, new_issuer text DEFAULT NULL,
                                          new_subject text DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
  partition_name text := 'chunks_' || new_user_id;
BEGIN
  INSERT INTO users (user_id, issuer, subject)
    VALUES (new_user_id, new_issuer, new_subject);
  EXECUTE format('CREATE TABLE %I PARTITION OF chunks FOR VALUES IN (%L)',
                 partition_name, new_user_id);
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO docsearch_app',
                 partition_name);
END;
$function$;

DROP FUNCTION app_libraries(text);
DROP FUNCTION role_rank(text);
DROP TABLE library_grants;
DROP TABLE libraries;
-- +goose StatementEnd
