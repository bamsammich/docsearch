-- Version 14: the authorization server's lists are JSON, not arrays.
--
-- Migration 13 stored redirect URIs, scopes and audiences as TEXT[]. The
-- server reads Postgres through database/sql, which has no array type, so a
-- TEXT[] column cannot be read back as a list without a driver-specific
-- wrapper. JSONB reads back as bytes through any driver. No release carried
-- version 13, so the tables are still empty wherever this runs.
--
-- +goose Up
-- +goose StatementBegin
ALTER TABLE oauth_clients        ALTER COLUMN redirect_uris TYPE JSONB USING to_jsonb(redirect_uris);
ALTER TABLE oauth_refresh_tokens ALTER COLUMN scopes        TYPE JSONB USING to_jsonb(scopes);
ALTER TABLE oauth_refresh_tokens ALTER COLUMN audience      TYPE JSONB USING to_jsonb(audience);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE oauth_clients ALTER COLUMN redirect_uris TYPE TEXT[]
  USING ARRAY(SELECT jsonb_array_elements_text(redirect_uris));
ALTER TABLE oauth_refresh_tokens ALTER COLUMN scopes TYPE TEXT[]
  USING ARRAY(SELECT jsonb_array_elements_text(scopes));
ALTER TABLE oauth_refresh_tokens ALTER COLUMN audience TYPE TEXT[]
  USING ARRAY(SELECT jsonb_array_elements_text(audience));
-- +goose StatementEnd
