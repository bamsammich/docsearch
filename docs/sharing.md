# Sharing libraries and the library UI

Personal and shared libraries, groups, and the web page docsearch-server serves. Back to the [README](../README.md).

## Sharing a library

Every account has a personal library. A shared library is one a team
searches and adds to together. Anyone signed in can create one and becomes
its admin.

```bash
docsearch library create platform --name "Platform docs"
docsearch library grant platform alice --role editor   # invites alice
docsearch library members platform
docsearch library revoke platform alice   # revoke your own to leave
docsearch library log platform            # who changed which role, for admins
docsearch library rename platform --name "Platform team docs"
docsearch library delete platform         # restorable for 14 days
docsearch library deleted                 # what you can still restore
docsearch library restore platform
```

Granting a role to someone who has none invites them. Until they accept,
nothing in the library appears in their searches or their agents' searches:

```bash
docsearch library invitations
docsearch library accept platform
docsearch library decline platform
```

| role | may |
|---|---|
| reader | search and read the library |
| editor | also add, re-read and remove documents |
| admin | also invite people, change and revoke roles, read the log, and rename, delete or restore the library |

Someone can be invited once they have signed in at least once, which is when
their username is recorded. Granting to someone already invited changes the
role offered, and revoking withdraws the invitation.

Renaming changes the name people read; the slug agents type stays. Deleting
takes a shared library out of every member's search at once and fails its
queued jobs. Any of its admins can restore it for 14 days, after which the
worker removes it and everything in it, and its slug is free again. A
personal library cannot be deleted. A library always keeps an admin, so the
last admin cannot leave or step down until they make someone else admin.

### Groups

A group shares a library with several people at once. Whoever creates a
group maintains it and invites people by username; each accepts or declines,
as with a library. A maintainer who administers a library grants it to the
group, and every member then reaches it. A person's role on a library is the
highest of their own and each group's.

```bash
docsearch group create platform-team --name "Platform team"
docsearch group invite platform-team alice              # --role maintainer
docsearch group accept platform-team                    # as alice
docsearch library grant platform --group platform-team --role editor
docsearch library members platform                      # people and groups
docsearch library revoke platform --group platform-team
```

Anyone signed in sees every group's name; only its members see who is in it.
A group gets reader or editor on a library, never admin, so managing a
library's members, renaming it and deleting it take your own admin grant. A
group always keeps a maintainer. Deleting a group takes its grants away and
leaves its libraries.

Administrators manage any group, and invite through the same invitation.
To add someone at once, with no invitation to accept, an administrator runs
`docsearch group invite <group> <username> --force`. The group's log and the
log of each library it holds record it, and it is the only way an
administrator joins a group they are not in.

Everyone may create groups unless `--group-creators administrators`
(`DOCSEARCH_GROUP_CREATORS`, `groups.creators` in `docsearch.yaml`) leaves it
to docsearch administrators.

An agent's search covers every library its user reads. `list_documents`
names each library, and a document outside your own is `library/doc_id`.
`add_document`, `upload_link` and `PUT /upload?library=` add to a library you
edit.

The CLI reads your own library unless `--library` names another, and takes a
`library/doc_id` wherever it takes a `doc_id`:

```bash
docsearch list --library platform
docsearch enqueue https://example.com/docs --library platform
docsearch jobs --library platform
docsearch verify platform/install-guide
```

### Copying and moving documents

A copy puts a document into another library by reading its original again,
so the two libraries share no rows. It keeps its `doc_id` unless `--as`
names another. A move is a copy that removes the document from where it was
once the copy is ready.

```bash
docsearch copy runbooks/pager --to platform              # reader of runbooks, editor of platform
docsearch move platform/pager --to ops --as ops-pager    # editor in both
docsearch jobs --library ops                             # the copy is a job
```

| original | what the copy reads |
|---|---|
| an upload | the stored file, copied into the new library |
| a crawled site | the site's cached pages, copied when you edit the library it is in, so no request goes to the site; otherwise the new library crawls it |
| a file under a root | the same path |

A copy is refused when the target library already holds the same file,
already reads the same source, or files another document under the
`doc_id`. A moved document's old identifier finds nothing afterwards. If you
stop editing the library a document is leaving before its copy is ready,
the copy still lands and the original stays, and the job says so.

## The library UI

`docsearch-server` serves a web page at its public URL (`--public-url`, printed
at startup as `library_ui`). It shows which documents your agents can search
and how well, the ingest queue, and a search field that calls the same MCP
`search` tool an agent does.

The library picker at the top of the side navigation chooses which library
the pages show, and creates a new one. Invitations wait at the top of the
picker, to accept or decline, and a library you deleted waits at the bottom
to restore. Search covers every library you read
unless its scope picker names one library or one document.

| page | what it answers |
|---|---|
| Documents | A table of documents with problems first, and beside it the selected document's checks: what an agent loses on each, and on which pages. A coverage bar splits every document into what agents reach by chapter, what they reach without usable chapters, and what never reached the index. Rename a document here; its `doc_id` stays the same. Re-ingest reads the file again even when it is unchanged, so a document picks up the current extraction and grading. |
| Reader | A document's chunks in order, as agents receive them, beside its outline. Chunks without a heading and pages that never reached the index are marked. A search result opens the reader at its chunk. Original opens the file the document was read from. |
| Ingest | Add a site or upload a file, with an optional title, and follow each job through discover, fetch, extract, chunk and index. Failures are listed first. |
| Members | Who may use the library and in which role. An admin invites someone by username, sees who has not yet accepted, changes a role, removes a member or withdraws an invitation, reads the log of changes, and renames or deletes the library. Anyone but the last admin can leave. |
| People | For administrators, from the account menu: every account, and in `password` mode adding, resetting, unlocking and removing them. |
| Sign-in | For administrators: how people sign in, and the flag or environment variable that sets each part. |
| Search | The field at the top of every page, focused with `/`. Search the whole library or one document, narrow by section, and see the ranked results and the raw response an agent receives. |

The page signs in with the credential an MCP client uses, and the server
keeps no session for it:

| `--auth` | in the browser |
|---|---|
| `none` | no sign-in |
| `token` | paste `DOCSEARCH_TOKEN`; it stays in that tab's `sessionStorage` |
| `oidc` | authorization code with PKCE at the provider, as a public client named by `--ui-client-id`, with `<public URL>/` as its redirect URI |
| `password` | authorization code with PKCE at docsearch itself; the page registers itself once as a public client at `/register` and keeps the client id in the browser |

The page and its assets carry no data and need no credential. Every read and
write it makes goes through the same guarded routes as an MCP client. The
server's own public origin is admitted past the Origin allowlist, so open the
page at that address: one reached through another hostname is refused until
that origin is added to `--allowed-origins`.


Building and developing the UI is covered in [CONTRIBUTING.md](../CONTRIBUTING.md#the-library-ui).
