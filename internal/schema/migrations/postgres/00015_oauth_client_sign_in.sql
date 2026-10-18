-- Version 15: a client records when it first signed someone in.
--
-- Registration is open, as the MCP specification expects, so a client that
-- registers and never signs anyone in is the trace of a caller filling the
-- table. Recording the first sign-in is what lets those be cleared without
-- touching a client someone uses. Every client already here is counted as
-- used, since this migration cannot tell.
--
-- +goose Up
-- +goose StatementBegin
ALTER TABLE oauth_clients ADD COLUMN signed_in_at TIMESTAMPTZ;
UPDATE oauth_clients SET signed_in_at = created_at;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE oauth_clients DROP COLUMN signed_in_at;
-- +goose StatementEnd
