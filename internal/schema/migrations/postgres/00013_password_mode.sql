-- Version 13: password mode. docsearch holds its own accounts and issues its
-- own tokens, for a deployment that has no identity provider.
--
-- password_accounts maps a username to the account it signs in as. The
-- account is an ordinary users row, created through create_account, so a
-- password account's library is partitioned and isolated like any other. Its
-- identity pair is ('docsearch', user_id): a token docsearch issues names
-- the user_id as its subject, and the pair keeps that from colliding with an
-- account a provider signs in.
--
-- The oauth_ tables are the authorization server's working state: clients
-- that registered, authorization requests in flight, refresh tokens, and
-- the key tokens are signed with. None is a tenant table. The server reads
-- them before any user is known, and only the server reads them at all, so
-- they carry no row-level security and the request role is granted them
-- directly.
--
-- A refresh token and a code are stored by hash, never as issued, so a
-- leaked table does not leak a working credential. The signing key is stored
-- as it is: anyone who can read this database can already read every
-- library in it.
--
-- +goose Up
-- +goose StatementBegin
CREATE TABLE password_accounts (
  username        TEXT PRIMARY KEY CHECK (username ~ '^[a-z0-9._-]{1,64}$'),
  user_id         TEXT NOT NULL UNIQUE REFERENCES users(user_id) ON DELETE CASCADE,
  password_hash   TEXT NOT NULL,
  failed_attempts INTEGER NOT NULL DEFAULT 0,
  locked_until    TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE oauth_clients (
  client_id     TEXT PRIMARY KEY,
  client_name   TEXT NOT NULL,
  redirect_uris TEXT[] NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE oauth_auth_requests (
  id         TEXT PRIMARY KEY,
  client_id  TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
  request    JSONB NOT NULL,
  user_id    TEXT REFERENCES users(user_id) ON DELETE CASCADE,
  auth_time  TIMESTAMPTZ,
  code_hash  TEXT UNIQUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE oauth_refresh_tokens (
  token_hash TEXT PRIMARY KEY,
  client_id  TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  scopes     TEXT[] NOT NULL,
  audience   TEXT[] NOT NULL,
  auth_time  TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE oauth_keys (
  key_id      TEXT PRIMARY KEY,
  private_key BYTEA NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE
  ON password_accounts, oauth_clients, oauth_auth_requests, oauth_refresh_tokens, oauth_keys
  TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
REVOKE ALL
  ON password_accounts, oauth_clients, oauth_auth_requests, oauth_refresh_tokens, oauth_keys
  FROM docsearch_app;
DROP TABLE oauth_keys;
DROP TABLE oauth_refresh_tokens;
DROP TABLE oauth_auth_requests;
DROP TABLE oauth_clients;
DROP TABLE password_accounts;
-- +goose StatementEnd
