# Ingesting documents

How documents get into a docsearch library: the CLI, uploads, documentation sites and the Paperless-ngx sync, and how each document's structure is derived. Back to the [README](../README.md).

## The CLI

```bash
docsearch migrate [--check] [--dsn DSN]            # bring the schema up to date
docsearch inspect <target>                         # what structure does it offer?
docsearch add     <target> [--title T]             # synchronous; file, directory or URL
docsearch enqueue <target> [--title T] [--force]   # queue for the worker
docsearch refresh <doc_id> [--from-cache]          # re-crawl an ingested site
docsearch jobs    [--all] [--limit N]              # queue state
docsearch cancel  <job_id>                         # ask a job to stop
docsearch list
docsearch remove  <doc_id>
docsearch copy    <doc_id> --to LIBRARY [--as ID]   # into another library
docsearch move    <doc_id> --to LIBRARY [--as ID]
docsearch verify  <doc_id>                         # did it chunk well enough?
```

A target is a path or an `http(s)` URL. `ingest` is a second name for `add`.

Every command but `migrate` is a client of `docsearch-server`, which is what
lets the index sit on another host and what makes one token the only way in.
`--server` says where it is, `DOCSEARCH_SERVER` is the same setting, and
`DOCSEARCH_TOKEN` carries the token, which has no flag because an argument is
visible to every process on the host through `ps`.

`migrate` is the exception: it opens the database directly, because it writes
the schema the server expects to find and so has to run before the server
does. `docsearch-worker` is a separate binary for the same reason, and runs
beside the index it writes.

Three commands answer three different questions, and a document can pass one
while failing another:

| | question |
|---|---|
| `inspect` | before ingest — what structure can be derived, and will this work at all? |
| `verify` | after ingest — did chunking produce something searchable? |
| `docsearch-eval --self-label` | does retrieval function on this index at all? |

**`inspect` first on anything unfamiliar.** It reports the text layer, the
embedded outline, whether a printed contents page can be reconstructed, the
font hierarchy and the share of the document that is running furniture — then
names the structure source that would be selected. A scanned PDF is told to run
OCR rather than described as structureless.

`ingest` and the worker call the same function; only the driver differs.

**Run `verify` after every ingest.** It answers two separate questions.

*Is the database consistent* — chunk counts, ordinal gaps, locator coverage,
FTS row parity. Reported under `PROBLEMS`.

*Did the document chunk well enough to be searchable* — reported as a verdict
of `good`, `degraded` or `unusable`, with a finding for each defect stating its
evidence and what it costs the caller. Every defect it grades is compatible
with a clean ingest: a document can be perfectly consistent, reach
`status='ready'`, and still be shaped so retrieval cannot work on it.

The one that matters most is `unaddressable`. When structure cannot be derived,
chunks are cut on the token budget alone — so every chunk is legally sized, no
size check fires, and consecutive slices inherit a single heading. A 70-page
manual can arrive as 35 well-formed chunks sharing 10 headings between them,
four of them headless. `section_filter` cannot separate those and `outline`
describes the whole document in ten entries. That is silent degradation to
fixed windows, and it is the failure this command exists to make loud.

A long section is not that failure. The token budget splits it, and every
piece keeps the section's heading path, so a paper with twenty Markdown
headings can arrive as 44 chunks under 20 paths and lose nothing. Few paths
per chunk count against a document only when its headings came from a source
that can miss some: a PDF's type sizes, an outline, a table of contents, a
site's URLs. Headings read from Markdown, HTML or Word markup count against
it only when one path holds half the document or more.

A document is invisible to search until its ingest completes. Every read path
filters `documents.status='ready'`.

### Re-ingesting unchanged bytes

An ingest hashes its source first. When a ready document already holds those
bytes, the ingest stamps it as read and stops, so a sync or a repeated
`enqueue` of a directory costs a hash per file. That shortcut would also stop
a change to extraction, chunking or grading from ever reaching a document
whose file never changed, so two things get past it.

| | reads held bytes again when |
|---|---|
| `docsearch enqueue --force`, and Re-ingest in the library UI | always |
| any other ingest | the document's structure report was written by an older pipeline |

Every structure report records the `domain.PipelineVersion` that wrote it,
and a change to an adapter, the chunker or the grader bumps that number. A
report from before versions were recorded counts as older than any build.
The re-read replaces the document that holds the bytes and keeps its
`doc_id`, path and title unless `--title` names another. The old version stays
searchable until the new one is published in its place, and `verify`,
`list_documents` and the library UI then show the new grade.

**Cost:** after a bump, each held document is read again the next time its
bytes arrive, once. A directory `enqueue` re-reads every file in it, and a
site `refresh --from-cache` re-chunks the site instead of reporting it
unchanged. `docsearch-sync` skips a document by timestamp before it sends
anything, so a bump reaches synced documents only when Paperless changes them
or they are re-ingested.

## Uploading a file

`add_document` reads files under the library roots, which a remote server
cannot see on your computer. A file from anywhere else goes through an
upload. The upload stores the file in your own rows of the database and
queues it like any other ingest, so a refresh can read it again later.

| caller | how |
|---|---|
| an agent in a chat | the `upload_link` tool returns a single-use link; open it in a browser and pick the file |
| an agent with a shell | `curl -T manual.pdf '<link>?name=manual.pdf'` against that link |
| a program with a token | `curl -T manual.pdf -H "Authorization: Bearer $DOCSEARCH_TOKEN" '<server>/upload?name=manual.pdf'` |

A link works once and for 15 minutes, and the database keeps only a hash of
it. The authenticated `PUT /upload` also takes `?key=`: a later upload under
the same key replaces that document instead of adding a second. Without a
key, the file's content hash is the key, so uploading the same bytes twice
gives one document. `?title=` names the document; without it, the document
takes the title it gives itself. `?library=<name>` puts the file in a shared
library where your role is editor or admin, instead of your own, and
`upload_link` and `add_document` take the same `library`.

`GET /original/<doc_id>` returns the file a document was read from, to the
account that owns it. A PDF comes back as a PDF and a Word file as a download.
Every other format comes back as plain text, so an uploaded HTML file never
runs in the browser. A site's original redirects to its URL.

`--public-url` (`DOCSEARCH_PUBLIC_URL`) is where a browser reaches the
server, and links are built on it. It defaults to the origin of
`--oidc-resource` in `oidc` mode, and to loopback on the listening port
otherwise. `--upload-max-mb` (`DOCSEARCH_UPLOAD_MAX_MB`) caps a file at 100
MB unless set.

## Ingesting a documentation site

```bash
docsearch inspect https://docs.example.com/docs    # dry run: crawl, report, write nothing
docsearch add     https://docs.example.com/docs    # crawl, chunk, index
docsearch refresh <doc_id>                         # transfers only what changed
docsearch refresh <doc_id> --from-cache            # re-chunk, no requests at all
```

**One site is one document.** Its navigation declares which sections exist and
how they nest, exactly as a PDF's embedded outline does, so each page becomes
an authoritative section keyed by its position in the nav tree and the page's
own `h1`–`h6` nesting subdivides beneath it. The chunker is untouched: a page
is a `Block.section` it already knows what to do with. Results carry the URL
and the in-page anchor they were read from, so an answer can be cited.

**Which pages exist and how they nest are different questions**, answered from
different sources, because neither source answers both. Coverage comes from
`sitemap.xml`, the seed page's own links and `llms.txt`, unioned — each is
incomplete in its own way. Hierarchy comes from a rendered sidebar, a hub
page's heading grouping, or URL path depth.

**A hierarchy source is checked against coverage before it is believed.** One
probed target renders 19 sidebar links against 210 pages because its generator
collapses categories in the browser; believing it would index a tenth of the
site under a confident-looking tree. Below half the page set, the source is
rejected and pages are nested by URL path instead — reported as inferred, with
nothing corroborating it.

**Pages no navigation source mentions are placed, never dropped.** On one
probed target that is 41 of 210 pages including every command reference.

**A crawl that mostly failed is refused rather than indexed.** An index over a
fraction of a site is legally formed, internally consistent, and answers
confidently from the part it happens to hold — and nothing a caller can see
reveals the rest is missing. A handful of broken links is ordinary and is
reported; past a share of them the ingest fails. That threshold is provisional
and says so in the code: it wants a doc-site corpus that does not exist yet.

Fetching obeys `robots.txt`, spaces requests to one host, identifies itself,
and holds raw responses in a **separate, disposable database** beside the
index — which is what makes `--from-cache` re-chunk a whole site offline, and
what makes a cancelled crawl resume rather than restart.

**The URL is a security boundary**, held to the standard the library roots are.
Scheme, host and every resolved address are validated before a request, on
every redirect hop, and again in the worker: a job row is not proof that
anything validated it. The rules live in `internal/urlguard`, with
`testdata/urlguard-addresses.txt` as the
table it is held to.
Every rejection returns the same error, so the fetcher cannot be used to map
an internal network.

Client-rendered pages are named by `inspect` rather than ingested as empty
shells. Headless rendering is not implemented.

## Where structure comes from

Chunk boundaries are only as trustworthy as the source they came from, so
sources are ranked by how much of what they assert is declared by the document
rather than inferred from it. A PDF is tried in this order:

| | source | supplies | corroboration |
|---|---|---|---|
| 1 | embedded outline, when an entry points at a page | sections, nesting, and the page each begins on | none — nothing it asserts is inferred |
| 2 | printed table of contents | sections and nesting | body headings must agree exactly, or ingest fails |
| 3 | font-size hierarchy | sections, nesting, position | no independent source exists to check it against |
| 4 | none of the above | — | refuse, rather than fall back to fixed windows |

Tier 1 needs no corroboration because there is nothing more reliable to check
it against — the author wrote the bookmarks. An outline whose every entry is a
web link points at no page, places no heading, and is skipped. Tier 2 is equally author-declared
but recovered by parsing a printed page, so the parse is verified against the
body and a disagreement fails the ingest. Tier 3 is inference all the way down.

An operator can name a [heading judge](judge.md) to check tiers 2 and 3. The
judge can only take out a heading the rules found, such as a procedure step
set at a heading size, and never a section the printed contents declare.

**A source that wins also supplies positions.** An outline's page numbers place
its sections directly, with the entry title matched against that page to find
the offset within it, over two lines when the title wraps. This matters for documents that style headings by weight
or colour rather than size: there is no font hierarchy to detect, and requiring
one would reject a document whose structure is fully declared.

Positions follow reading order. A page set in two columns of running text is
read a column at a time, so a heading in the right column starts its section
where a reader reaches it. A table, whose cells leave their columns mostly
empty, keeps its rows.

Non-PDF formats sit at tier 1 by construction — a Markdown `##`, an HTML `<h2>`
and a DOCX heading style are all declarations, not inferences.

## Chunking rules

Chunking rules, in one place: the chunk unit is the deepest numbered section a
document declares, falling back to the deepest heading path when there is no
numbering. Units over ~1200 tokens subdivide; units under ~100 tokens merge
forward, but only when the document did not number them. A numbered boundary is
one the document declared, and small numbered sections stay small.

## Syncing from Paperless-ngx

`docsearch-sync` pushes the documents a Paperless-ngx user can see into a
docsearch library, and retires them when that user stops seeing them. It runs
beside Paperless and reaches out to both, so docsearch never needs a way into
the network Paperless lives on.

Which documents reach docsearch is decided in Paperless. Give a dedicated
Paperless user view permission on the documents worth searching, usually
with a workflow that shares chosen document types on consumption, and give
the sync that user's API token.

```bash
PAPERLESS_TOKEN=... DOCSEARCH_TOKEN=... \
  docsearch-sync --paperless-url http://paperless:8000 --server https://docs.example.com
```

| flag | environment | default |
|---|---|---|
| `--paperless-url` | `PAPERLESS_URL` | none |
| `--server` | `DOCSEARCH_SERVER` | `http://127.0.0.1:8765` |
| `--library` | `DOCSEARCH_SYNC_LIBRARY` | your own library; name a shared one you edit to fill it instead |
| `--key-prefix` | `DOCSEARCH_SYNC_KEY_PREFIX` | `paperless`; use one per Paperless instance |
| `--every` | `DOCSEARCH_SYNC_EVERY` | `0`, one pass and exit |
| `--prefer-archive` | `DOCSEARCH_SYNC_PREFER_ARCHIVE` | off |
| `--allow-empty` | | off |
| `--dry-run` | | off |

Nothing is kept on disk. A document is pushed when the library lacks it or
read it before Paperless last changed it, so the library itself is the
record of what was pushed. The original file is pushed where docsearch reads
its type, and Paperless's archive copy otherwise; `--prefer-archive` takes
the archive whenever there is one, which suits a library of scans. A
Paperless that shows nothing while the library holds documents this sync
pushed is refused rather than emptied, unless `--allow-empty` says so.
`Dockerfile.sync` builds an image for it.
