-- The authorization server's state. Read before any user is known, by the
-- server alone, so no row-level security applies.

-- name: InsertOAuthClient :exec
INSERT INTO oauth_clients (client_id, client_name, redirect_uris, created_at)
VALUES ($1, $2, $3, $4);

-- name: OAuthClient :one
SELECT client_id, client_name, redirect_uris, created_at FROM oauth_clients WHERE client_id = $1;

-- Saving a request also clears the ones abandoned long ago, so the table
-- holds only requests someone could still finish.
-- name: PruneOAuthRequests :exec
DELETE FROM oauth_auth_requests WHERE created_at < sqlc.arg(before)::timestamptz;

-- name: InsertOAuthRequest :exec
INSERT INTO oauth_auth_requests (id, client_id, request, created_at) VALUES ($1, $2, $3, $4);

-- name: OAuthRequest :one
SELECT request, user_id, auth_time FROM oauth_auth_requests WHERE id = $1;

-- name: OAuthRequestByCode :one
SELECT request, user_id, auth_time FROM oauth_auth_requests WHERE code_hash = $1;

-- name: SetOAuthCode :exec
UPDATE oauth_auth_requests SET code_hash = $2 WHERE id = $1;

-- name: CompleteOAuthRequest :exec
UPDATE oauth_auth_requests SET user_id = $2, auth_time = $3 WHERE id = $1;

-- name: DeleteOAuthRequest :exec
DELETE FROM oauth_auth_requests WHERE id = $1;

-- name: InsertRefreshToken :exec
INSERT INTO oauth_refresh_tokens
  (token_hash, client_id, user_id, scopes, audience, auth_time, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: RefreshToken :one
SELECT token_hash, client_id, user_id, scopes, audience, auth_time, expires_at
  FROM oauth_refresh_tokens WHERE token_hash = $1;

-- name: TakeRefreshToken :one
DELETE FROM oauth_refresh_tokens WHERE token_hash = $1
RETURNING token_hash, client_id, user_id, scopes, audience, auth_time, expires_at;

-- name: DeleteRefreshToken :exec
DELETE FROM oauth_refresh_tokens WHERE token_hash = $1;

-- name: DeleteRefreshTokensOf :exec
DELETE FROM oauth_refresh_tokens WHERE user_id = $1 AND client_id = $2;

-- name: OAuthKeys :many
SELECT key_id, private_key FROM oauth_keys ORDER BY created_at, key_id;

-- name: InsertOAuthKey :exec
INSERT INTO oauth_keys (key_id, private_key) VALUES ($1, $2) ON CONFLICT DO NOTHING;
