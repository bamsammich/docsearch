-- Managing shared libraries, through internal/repository/postgres.
--
-- Every statement runs in a transaction that named its user, and each calls
-- a definer function that checks that user's role before it acts. The
-- request role holds no write on libraries or library_grants.

-- name: CreateLibrary :one
SELECT create_library(sqlc.arg(slug)::text, sqlc.arg(name)::text)::text AS library_id;

-- name: GrantLibrary :one
SELECT grant_library(sqlc.arg(slug)::text, sqlc.arg(username)::text, sqlc.arg(role)::text)::text
  AS outcome;

-- name: RevokeLibrary :exec
SELECT revoke_library(sqlc.arg(slug)::text, sqlc.arg(username)::text);

-- name: LibraryMembers :many
SELECT m.username::text AS username, m.role::text AS role, m.invited::boolean AS invited
  FROM library_members(sqlc.arg(slug)::text) AS m(username, role, invited);

-- name: LibraryEvents :many
SELECT e.at::timestamptz AS at, e.actor::text AS actor, e.action::text AS action,
       e.subject::text AS subject, e.role::text AS role, e.detail::text AS detail
  FROM library_events(sqlc.arg(slug)::text) AS e(at, actor, action, subject, role, detail);

-- The request role reads users, so an account's own username needs no
-- definer.
-- name: OwnUsername :one
SELECT COALESCE(username, '')::text AS username
  FROM users WHERE user_id = current_setting('app.user_id', true);

-- name: MyInvitations :many
SELECT i.slug::text AS slug, i.name::text AS name, i.role::text AS role,
       i.invited_by::text AS invited_by, i.invited_at::timestamptz AS invited_at
  FROM my_invitations() AS i(slug, name, role, invited_by, invited_at);

-- name: AcceptInvitation :one
SELECT accept_invitation(sqlc.arg(slug)::text)::text AS role;

-- name: DeclineInvitation :exec
SELECT decline_invitation(sqlc.arg(slug)::text);

-- name: RenameLibrary :exec
SELECT rename_library(sqlc.arg(slug)::text, sqlc.arg(name)::text);

-- name: DeleteLibrary :one
SELECT delete_library(sqlc.arg(slug)::text)::timestamptz AS purge_at;

-- name: RestoreLibrary :exec
SELECT restore_library(sqlc.arg(slug)::text);

-- name: DeletedLibraries :many
SELECT d.slug::text AS slug, d.name::text AS name, d.deleted_at::timestamptz AS deleted_at,
       d.purge_at::timestamptz AS purge_at, d.deleted_by::text AS deleted_by
  FROM deleted_libraries() AS d(slug, name, deleted_at, purge_at, deleted_by);
