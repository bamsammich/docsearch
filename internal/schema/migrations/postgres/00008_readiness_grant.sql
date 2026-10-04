-- Version 8: the readiness probe can read the version it checks.
--
-- Migration 6 granted the app role the tenant tables and users, which is
-- what a request needs, and left out schema_version. The probe behind
-- /readyz connects as that role and reads the recorded version, so without
-- this it reports every migrated database as unversioned and the server
-- never comes up.
--
-- schema_version carries no policy, deliberately: the probe answers before
-- anyone has signed in, and a version number says nothing about a library.
--
-- +goose Up
-- +goose StatementBegin
GRANT SELECT ON schema_version TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
REVOKE SELECT ON schema_version FROM docsearch_app;
-- +goose StatementEnd
