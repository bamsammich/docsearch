# Pushing Paperless documents in from home

Phase 07 of v2. A Paperless-ngx instance on a home network holds documents
worth searching, and the docsearch server that would search them sits on the
internet-facing edge. Pulling from Paperless would mean exposing the
household archive to that edge, or storing Paperless tokens on the server
and fetching addresses a user names. So the documents are pushed out by
`docsearch-sync`, which runs beside Paperless and is a client of both. The
phase ends when a document shared with the sync's Paperless user shows up in
search, and unsharing it retires it.

## What reaches docsearch is decided in Paperless

The sync reads only what its Paperless user can see. Which documents that
is belongs to Paperless: grant the user view permission on the document
types worth searching, typically with a workflow that shares a type on
consumption. Retyping a document so the workflow no longer shares it is how
it leaves docsearch. Nothing in docsearch encodes a document type, a tag or
a household rule.

## No state on disk

Each Paperless document is one upload under the key `<prefix>-<id>`, through
the authenticated upload door phase 06 built. The library is then the record
of what was pushed, so a sync keeps nothing locally.

| decision | read from |
|---|---|
| push | Paperless shows it and the library lacks `upload:<prefix>-<id>`, or read it before Paperless last modified it |
| skip, unchanged | the library read it after Paperless last modified it |
| skip, already being read | a queued or running job holds the same source |
| retire | the library holds the key and Paperless no longer shows it |

A lost machine therefore loses nothing: the next sync reads both sides and
picks up where the last one stopped. The prefix lets two Paperless instances
feed one library without retiring each other's documents.

The document API gained `source` and `ingested_at` for this. A client that
put a document in can now find it again, and tell whether docsearch has read
the latest copy.

## Original or archive

Paperless keeps the file as it arrived and, where it ran OCR, an archive copy
with a text layer. The sync pushes the original where docsearch reads its
type, because an archive conversion can flatten the font sizes and outline
that docsearch derives structure from. For a file docsearch cannot read, such
as a photographed page, it pushes the archive. `--prefer-archive` pushes the
archive whenever there is one, which is the right setting for a library of
scans: a scanned PDF's original has no text for docsearch to read.

## Safeguards

An empty list from Paperless while the library holds documents this sync
pushed is refused, unless `--allow-empty` says it is meant. An empty list is
far more often a revoked share or a token for the wrong user than a library
someone emptied, and the alternative is retiring everything in one pass. One
document that fails to download or upload is reported, and the rest still
sync.

## Steps

| step | what lands | how it is checked |
|---|---|---|
| 7a | `docsearch-sync`, the Paperless client, `source` and `ingested_at` on the document API | the plan and run suites over mocked Paperless, library and uploader; a run against a real Paperless-ngx container, sharing a document and then unsharing it |
