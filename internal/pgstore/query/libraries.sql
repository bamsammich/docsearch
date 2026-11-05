-- Managing shared libraries, through internal/repository/postgres.
--
-- Every statement runs in a transaction that named its user, and each calls
-- a definer function that checks that user's role before it acts. The
-- request role holds no write on libraries or library_grants.

-- name: CreateLibrary :one
SELECT create_library(sqlc.arg(slug)::text, sqlc.arg(name)::text)::text AS library_id;

-- name: GrantLibrary :exec
SELECT grant_library(sqlc.arg(slug)::text, sqlc.arg(username)::text, sqlc.arg(role)::text);

-- name: RevokeLibrary :exec
SELECT revoke_library(sqlc.arg(slug)::text, sqlc.arg(username)::text);

-- name: LibraryMembers :many
SELECT m.username::text AS username, m.role::text AS role
  FROM library_members(sqlc.arg(slug)::text) AS m(username, role);

-- name: LibraryEvents :many
SELECT e.at::timestamptz AS at, e.actor::text AS actor, e.action::text AS action,
       e.subject::text AS subject, e.role::text AS role
  FROM library_events(sqlc.arg(slug)::text) AS e(at, actor, action, subject, role);

-- name: OwnUsername :one
SELECT COALESCE(own_username(), '')::text AS username;
