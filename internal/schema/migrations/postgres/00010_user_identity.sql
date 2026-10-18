-- Version 10: an account is an issuer and a subject, and creating one
-- creates the partition its chunks live in.
--
-- Two providers can both call someone 'alice', so a subject alone collides.
-- The pair is what an account is, and the unique index is what stops one
-- account being reachable through two identities.
--
-- Both columns stay nullable because the built-in user has neither. NULLS NOT
-- DISTINCT is what keeps that from becoming a hole: without it Postgres
-- treats every NULL as unique and any number of rows could carry no
-- identity at all, where exactly one should.
--
-- Which pair gets stored was settled by docs/research/mcp-authorization.md:
-- the provider is the authorization server, so the claims of the token it
-- issues are the identity. A deployment where docsearch mints its own tokens
-- stores the upstream provider's claims here instead, read once at first
-- sign-in, and never docsearch's own issuer.
--
-- +goose Up
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN issuer  text;
ALTER TABLE users ADD COLUMN subject text;

-- Half an identity is not one. A row with an issuer and no subject names
-- nobody, and would pass a unique index that only sees the pair.
ALTER TABLE users ADD CONSTRAINT users_identity_whole
  CHECK ((issuer IS NULL) = (subject IS NULL));

CREATE UNIQUE INDEX users_identity ON users (issuer, subject) NULLS NOT DISTINCT;

-- A user_id becomes part of a table name below, so it has to be safe to
-- quote and short enough to leave room for the prefix. An identifier is 63
-- bytes; 40 leaves room and is far more than an internal id needs. The
-- built-in 'default' passes, and nothing else creates users yet.
ALTER TABLE users ADD CONSTRAINT users_id_is_identifier_safe
  CHECK (user_id ~ '^[a-z0-9_-]{1,40}$');

-- Creating an account is privileged work, and the role a request runs as
-- cannot do it: it holds no CREATE on the schema and no INSERT on users, by
-- design, since both would let a request reach past its own library. A
-- function owned by the schema owner is the narrow way to let the request
-- path ask for exactly this one operation and nothing else.
--
-- The row and the partition are created together because a chunk for a user
-- with no partition is refused, so an account without one is an account that
-- cannot be written to. One transaction, or neither.
--
-- search_path is pinned: a SECURITY DEFINER function that resolves names
-- through the caller's path runs the caller's code with the owner's rights.
-- Identifiers go through format's %I and values through %L, so a user_id is
-- never concatenated into DDL.
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION create_account(new_user_id text, new_issuer text DEFAULT NULL,
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
-- +goose StatementEnd

-- +goose StatementBegin
-- EXECUTE is granted to the app role and to nobody else. REVOKE from PUBLIC
-- first: a new function is executable by PUBLIC by default, which would hand
-- account creation to every role in the database.
REVOKE ALL ON FUNCTION create_account(text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION create_account(text, text, text) TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION create_account(text, text, text);
DROP INDEX users_identity;
ALTER TABLE users DROP CONSTRAINT users_id_is_identifier_safe;
ALTER TABLE users DROP CONSTRAINT users_identity_whole;
ALTER TABLE users DROP COLUMN subject;
ALTER TABLE users DROP COLUMN issuer;
-- +goose StatementEnd
