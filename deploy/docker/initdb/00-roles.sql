-- What the cluster does once, before docsearch ever connects.
--
-- Three statements here need privileges docsearch deliberately does not
-- have. CREATE EXTENSION is superuser-only, CREATE ROLE needs CREATEROLE,
-- and the database has to exist before anything can be migrated into it.
-- docs/plans/postgres-multiuser.md carries the full boundary.
--
-- Postgres runs every file in /docker-entrypoint-initdb.d once, on an empty
-- data directory. A cluster that already holds data skips it, so changing
-- this file does nothing to a volume that exists.

-- The role that owns every table and runs migrations. NOSUPERUSER and
-- NOBYPASSRLS, so even the owner is bound by the policies, which is what
-- FORCE ROW LEVEL SECURITY asks for.
CREATE ROLE docsearch_owner LOGIN PASSWORD 'docsearch' NOSUPERUSER NOBYPASSRLS;

-- The role a request runs as. It owns nothing, so every privilege it has was
-- granted by a migration, and row-level security decides which rows each one
-- reaches.
CREATE ROLE docsearch_app LOGIN PASSWORD 'docsearch' NOSUPERUSER NOBYPASSRLS;

CREATE DATABASE docsearch OWNER docsearch_owner;

\connect docsearch

GRANT USAGE ON SCHEMA public TO docsearch_app;
CREATE EXTENSION pg_textsearch;
