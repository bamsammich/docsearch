-- Password accounts are read before any user is known, by the sign-in page,
-- and written by an operator through `docsearch account`. Neither names a
-- user, and the table carries no row-level security.

-- name: InsertPasswordAccount :exec
INSERT INTO password_accounts (username, user_id, password_hash) VALUES ($1, $2, $3);

-- name: PasswordAccountByUsername :one
SELECT username, user_id, password_hash, failed_attempts, locked_until, created_at
  FROM password_accounts WHERE username = $1;

-- name: PasswordAccountExists :one
SELECT EXISTS (SELECT 1 FROM password_accounts WHERE user_id = $1)::boolean AS present;

-- A failure past the threshold locks the account until lock_until.
-- name: RecordFailedSignIn :exec
UPDATE password_accounts
   SET failed_attempts = failed_attempts + 1,
       locked_until = CASE WHEN failed_attempts + 1 >= sqlc.arg(threshold)::int
                           THEN sqlc.arg(lock_until)::timestamptz ELSE locked_until END
 WHERE username = sqlc.arg(username);

-- name: ClearFailedSignIns :exec
UPDATE password_accounts SET failed_attempts = 0, locked_until = NULL WHERE username = $1;

-- Setting a password also lifts a lock, since it is how an operator lets
-- someone back in.
-- name: SetPasswordHash :execrows
UPDATE password_accounts
   SET password_hash = $2, failed_attempts = 0, locked_until = NULL
 WHERE username = $1;

-- name: DeletePasswordAccount :one
DELETE FROM password_accounts WHERE username = $1 RETURNING user_id;

-- Removing an account ends every sign-in it holds.
-- name: DeleteRefreshTokensOfUser :exec
DELETE FROM oauth_refresh_tokens WHERE user_id = $1;

-- name: ListPasswordAccounts :many
SELECT username, user_id, failed_attempts, locked_until, created_at
  FROM password_accounts ORDER BY username;

-- name: CreatePasswordUser :exec
SELECT create_account(sqlc.arg(user_id)::text, 'docsearch', sqlc.arg(user_id)::text);

-- name: ClaimBuiltinForPassword :one
SELECT COALESCE(claim_builtin('docsearch', 'default'), '')::text AS user_id;
