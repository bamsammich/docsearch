-- Managing groups, through internal/repository/postgres.
--
-- Every statement runs in a transaction that named its user, and each calls
-- a definer function that checks that user's place in the group before it
-- acts. The request role holds no write on any group table.

-- name: CreateGroup :one
SELECT create_group(sqlc.arg(slug)::text, sqlc.arg(name)::text)::text AS group_id;

-- name: RenameGroup :exec
SELECT rename_group(sqlc.arg(slug)::text, sqlc.arg(name)::text);

-- name: DeleteGroup :exec
SELECT delete_group(sqlc.arg(slug)::text);

-- name: InviteToGroup :one
SELECT invite_to_group(sqlc.arg(slug)::text, sqlc.arg(username)::text, sqlc.arg(role)::text)::text
  AS outcome;

-- name: ForceIntoGroup :exec
SELECT force_into_group(sqlc.arg(slug)::text, sqlc.arg(username)::text, sqlc.arg(role)::text);

-- name: AcceptGroupInvitation :one
SELECT accept_group_invitation(sqlc.arg(slug)::text)::text AS role;

-- name: DeclineGroupInvitation :exec
SELECT decline_group_invitation(sqlc.arg(slug)::text);

-- name: RemoveFromGroup :exec
SELECT remove_from_group(sqlc.arg(slug)::text, sqlc.arg(username)::text);

-- name: AllGroups :many
SELECT g.slug::text AS slug, g.name::text AS name, g.members::bigint AS members, g.role::text AS role
  FROM all_groups() AS g(slug, name, members, role);

-- name: MyGroupInvitations :many
SELECT i.slug::text AS slug, i.name::text AS name, i.role::text AS role,
       i.invited_by::text AS invited_by, i.invited_at::timestamptz AS invited_at
  FROM my_group_invitations() AS i(slug, name, role, invited_by, invited_at);

-- name: GroupMembers :many
SELECT m.username::text AS username, m.role::text AS role, m.source::text AS source,
       m.invited::boolean AS invited
  FROM group_members_of(sqlc.arg(slug)::text) AS m(username, role, source, invited);

-- name: GroupLibraries :many
SELECT l.slug::text AS slug, l.name::text AS name, l.role::text AS role
  FROM group_libraries(sqlc.arg(slug)::text) AS l(slug, name, role);

-- name: GroupEvents :many
SELECT e.at::timestamptz AS at, e.actor::text AS actor, e.action::text AS action,
       e.subject::text AS subject, e.role::text AS role, e.detail::text AS detail
  FROM group_events_of(sqlc.arg(slug)::text) AS e(at, actor, action, subject, role, detail);

-- name: GrantLibraryGroup :exec
SELECT grant_library_group(sqlc.arg(library)::text, sqlc.arg(slug)::text, sqlc.arg(role)::text);

-- name: RevokeLibraryGroup :exec
SELECT revoke_library_group(sqlc.arg(library)::text, sqlc.arg(slug)::text);

-- name: LibraryGroups :many
SELECT g.slug::text AS slug, g.name::text AS name, g.role::text AS role, g.members::bigint AS members
  FROM library_groups(sqlc.arg(library)::text) AS g(slug, name, role, members);

-- name: LinkGroup :exec
SELECT link_group(sqlc.arg(slug)::text, sqlc.arg(provider_group)::text);

-- The provider group a group follows, for an administrator; admin is
-- false for anyone else, and provider_group then empty.
-- name: GroupProviderLink :one
SELECT COALESCE(group_provider_link(sqlc.arg(slug)::text), '')::text AS provider_group,
       COALESCE((SELECT admin FROM users WHERE user_id = current_setting('app.user_id', true)),
                false)::boolean AS admin;

-- The two lists of names arrive as JSON arrays, which database/sql passes
-- as one value.
-- name: SyncProviderGroups :one
SELECT sync_provider_groups(
  sqlc.arg(user_id)::text,
  ARRAY(SELECT jsonb_array_elements_text(sqlc.arg(carried)::jsonb)),
  ARRAY(SELECT jsonb_array_elements_text(sqlc.arg(create_missing)::jsonb)))::integer AS changed;
