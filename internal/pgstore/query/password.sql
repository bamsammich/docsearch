-- Password accounts are read before any user is known, by the sign-in page,
-- and the table carries no row-level security. Adding, resetting and
-- removing one goes through definer functions, which refuse anyone but an
-- administrator or the owner role.

-- name: AddPasswordAccount :exec
SELECT add_password_account(sqlc.arg(username)::text, sqlc.arg(user_id)::text,
                            sqlc.arg(password_hash)::text);

-- name: PasswordAccountByUsername :one
SELECT username, user_id, password_hash, failed_attempts, locked_until, created_at
  FROM password_accounts WHERE username = $1;

-- name: PasswordAccountExists :one
SELECT EXISTS (SELECT 1 FROM password_accounts WHERE user_id = $1)::boolean AS present;

-- An attempt is counted before its password is checked, in the statement that
-- reads whether the account is locked, so guesses sent at once cannot all slip
-- in under the threshold. A lock that has run out starts the count again, so
-- a single wrong guess cannot hold an account locked indefinitely. No row is
-- a locked account: the attempt is refused and counts for nothing.
-- name: BeginSignIn :one
UPDATE password_accounts
   SET failed_attempts = CASE WHEN locked_until IS NULL THEN failed_attempts + 1 ELSE 1 END,
       locked_until = CASE
           WHEN (CASE WHEN locked_until IS NULL THEN failed_attempts + 1 ELSE 1 END)
                >= sqlc.arg(threshold)::int
           THEN sqlc.arg(lock_until)::timestamptz END
 WHERE username = sqlc.arg(username)
   AND (locked_until IS NULL OR locked_until <= sqlc.arg(now)::timestamptz)
RETURNING failed_attempts;

-- name: ClearFailedSignIns :exec
UPDATE password_accounts SET failed_attempts = 0, locked_until = NULL WHERE username = $1;

-- Setting a password also lifts a lock, since it is how an operator lets
-- someone back in.
-- name: SetPasswordHash :execrows
UPDATE password_accounts
   SET password_hash = $2, failed_attempts = 0, locked_until = NULL
 WHERE username = $1;

-- Setting a password for someone also ends their sign-ins. found is false
-- when no account holds the name.
-- name: ResetPassword :one
SELECT reset_password(sqlc.arg(username)::text, sqlc.arg(password_hash)::text)::boolean AS found;

-- A removal names the administrator who takes over the shared libraries
-- only the removed account administered, for the trigger that hands them on.
-- name: NameHeir :exec
SELECT set_config('app.heir', sqlc.arg(heir)::text, true);

-- Removing an account ends every sign-in it holds. found is false when no
-- account holds the name.
-- name: RemovePasswordAccount :one
SELECT remove_password_account(sqlc.arg(username)::text)::boolean AS found;

-- name: ListPasswordAccounts :many
SELECT username, user_id, failed_attempts, locked_until, created_at
  FROM password_accounts ORDER BY username;

-- name: CreatePasswordUser :exec
SELECT create_account(sqlc.arg(user_id)::text, 'docsearch', sqlc.arg(user_id)::text);

-- name: ClaimBuiltinForPassword :one
SELECT COALESCE(claim_builtin('docsearch', 'default'), '')::text AS user_id;
