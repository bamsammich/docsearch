# Getting a file in when no path reaches it

Phase 06 of v2. A tool call cannot carry a file, because its arguments are
generated a token at a time and a 10MB PDF never fits. `add_document` only
reads paths under the library roots, which a remote server cannot see on
anyone's computer. So a file comes in by a side door, and the phase ends when
a PDF uploaded from claude.ai and one uploaded from Claude Code both reach
`ready`.

## Two doors, one path after them

| door | credential | for |
|---|---|---|
| `GET`/`PUT`/`POST /upload/<token>` | the token in the link | a person in a browser, or an agent running `curl -T` |
| `PUT /upload` | the server's guard: bearer token or OIDC | a program, such as `docsearch-sync` |

The `upload_link` MCP tool issues the first kind. A link is a random token,
single-use, good for 15 minutes, and the database keeps only its SHA-256.
The link has to be its own credential, since a browser opening it cannot
attach a bearer token. A leaked `upload_links` table must therefore not leak
working links.

Both doors store the file and queue `upload:<key>`. The key is what replaces
what. The authenticated door takes `?key=`, which is how a sync replaces the
document it uploaded last time, and otherwise the key is the content hash.
The same bytes uploaded twice are therefore one document.

## The bytes live in Postgres

Migration 12 adds `uploads`, a tenant table under the same row-level security
as every other, and `upload_links`. The server that receives a file and the
worker that reads it are separate processes, often on separate hosts. The
database is the one thing both already share, and its policies already keep
one user's files from another. A filesystem would need a volume both could
mount and an isolation rule of its own.

Redeeming a link happens before any user is known, so it goes through
`redeem_upload_link`, a definer function that marks the link used in the
statement that reads it, so two uploads racing on one link cannot both win.
`FORCE ROW LEVEL SECURITY` binds the owner the function runs as. A second
policy lets that role, and only that role, see every link. Requests never run
as it.

The worker reads an upload back through `internal/source/uploaded`, which
writes the bytes to a private temporary directory under the file's own name,
because an adapter reads a path and its suffix picks the adapter, and
removes it after extraction.

## Decided along the way

| decision | why |
|---|---|
| read and check the file before spending the link | a person who picks an unreadable file keeps the link to try another |
| 100 MB default cap, `--upload-max-mb` to change it | large enough for the biggest manual docsearch has read, small enough that one upload cannot fill a database by accident |
| `--public-url`, defaulting to the `oidc` resource origin or to loopback | a link has to name an address the person's browser reaches |
| the link door sits outside the origin allowlist | the page posts to itself from docsearch's own origin, and the token is the credential |
| no pasted-text path | text an agent pastes is its own summary as often as a source, and the corpus holds sources only |
| no URL-mode elicitation yet | which clients support it is unverified; the link does the same job today |

## Steps

| step | what lands | how it is checked |
|---|---|---|
| 6a | migration 12, both doors, the upload source, `upload_link` | the door suite over mocked links and store; the Postgres suite for isolation and single use; one PDF through the live stack by `curl -T` |
| 6b | the same upload from claude.ai | owed: it needs a person and a deployment claude.ai can reach |
