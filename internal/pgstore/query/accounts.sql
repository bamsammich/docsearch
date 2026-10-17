-- Accounts are read before any user is known, so these run on a bare
-- connection. users carries no row-level security: an identity is not a
-- library, and resolving one is how a request learns whose library it reads.

-- name: AccountByIdentity :one
SELECT user_id FROM users
 WHERE issuer = sqlc.arg(issuer)::text AND subject = sqlc.arg(subject)::text;

-- name: CreateAccount :exec
SELECT create_account(sqlc.arg(user_id)::text, sqlc.arg(issuer)::text, sqlc.arg(subject)::text);

-- claim_builtin answers NULL once the library has an owner. The empty string
-- stands in for it so the result scans as a string.
-- name: ClaimBuiltin :one
SELECT COALESCE(claim_builtin(sqlc.arg(issuer)::text, sqlc.arg(subject)::text), '')::text
  AS user_id;
