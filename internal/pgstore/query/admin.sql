-- Administration, through internal/repository/postgres. The request role
-- reads users, so the reads are plain queries; the two writes go through the
-- definer functions migration 24 created, which check that the transaction's
-- user is an administrator.

-- name: IsAdmin :one
SELECT COALESCE(
  (SELECT admin FROM users WHERE user_id = current_setting('app.user_id', true)),
  false)::boolean AS admin;

-- Every account an administrator manages: each one but the built-in user
-- nobody signs in as.
-- name: AccountList :many
SELECT COALESCE(username, '')::text AS username, created_at, admin
  FROM users
 WHERE user_id <> 'default' OR username IS NOT NULL
 ORDER BY username NULLS LAST, created_at;

-- name: SetAdmin :exec
SELECT set_admin(sqlc.arg(username)::text, sqlc.arg(make)::boolean);

-- name: RecordAccountEvent :exec
SELECT record_account_event(sqlc.arg(action)::text, sqlc.arg(username)::text);

-- The owner DSN makes the first administrator, through the CLI, before
-- anyone can use the functions above.
-- name: GrantAdminAsOwner :execrows
UPDATE users SET admin = sqlc.arg(make)::boolean WHERE username = sqlc.arg(username)::text;
