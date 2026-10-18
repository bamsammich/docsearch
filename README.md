# docsearch

Local document search over an MCP server. Documents are ingested into a
Postgres database by a background worker; a Go server reads that database and
exposes search, navigation and retrieval tools over MCP Streamable HTTP.

No vector database, no embeddings, no external services. BM25 over
`pg_textsearch`.
[An embedding reranker was measured and rejected](docs/research/embedding-rerank-probe.md).

**This is v2:** a Go rewrite with multi-user storage on Postgres, pluggable
authentication, uploads and a Paperless-ngx sync. v1 lives on the
[`v1` branch](https://github.com/bamsammich/docsearch/tree/v1) and takes
security fixes only, until six months after `v2.0.0`.
[Upgrading from v1](#upgrading-from-v1) moves a v1 library across.

docsearch is built from a checkout or from its container images. Its module
path carries no `/v2`, so `go install ...@latest` still resolves to `v1.0.0`.

## Three processes

| Process | Lifetime | Role |
|---|---|---|
| Ingest worker | daemon | claims queued jobs, extracts, chunks, writes |
| MCP server | daemon | reads the database, enqueues jobs, reports status |
| CLI | one-shot | manual ingest, listing, removal, verification |
| Paperless sync | one-shot or daemon, optional | pushes what a Paperless-ngx user can see; runs beside Paperless |

Ingest never runs inside the server process: extraction saturates a core for
minutes and would starve request handling.

## Setup

```bash
mise install          # go 1.26, node 22, golangci-lint, buf, sqlc, mockery
mise run build        # the library UI, then docsearch-server, docsearch-worker, docsearch, docsearch-sync
```

`pdffonts` (poppler) is useful for inspecting a PDF's text layer and is not in
the mise registry — install it from your package manager.

## Ingesting

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
takes the title it gives itself.

`GET /original/<doc_id>` returns the file a document was read from, to the
account that owns it. A PDF comes back as a PDF and a Word file as a download.
Every other format comes back as plain text, so an uploaded HTML file never
runs in the browser. A site's original redirects to its URL.

`--public-url` (`DOCSEARCH_PUBLIC_URL`) is where a browser reaches the
server, and links are built on it. It defaults to the origin of
`--oidc-resource` in `oidc` mode, and to loopback on the listening port
otherwise. `--upload-max-mb` (`DOCSEARCH_UPLOAD_MAX_MB`) caps a file at 100
MB unless set.

## The library UI

`docsearch-server` serves a web page at its public URL (`--public-url`, printed
at startup as `library_ui`). It shows which documents your agents can search
and how well, the ingest queue, and a search preview that calls the same MCP
`search` tool an agent does.

| tab | what it answers |
|---|---|
| Library | Which documents need attention, and what an agent loses on each. A coverage strip splits every document into pages reachable by chapter, pages reachable without usable chapters, and pages that never reached the index. Rename a document here; its `doc_id` stays the same. Re-ingest reads the file again even when it is unchanged, so a document picks up the current extraction and grading. |
| Reader | A document's chunks in order, as agents receive them, beside its outline. Chunks without a heading and pages that never reached the index are marked. A search result opens the reader at its chunk. Original opens the file the document was read from. |
| Ingest | Add a site or upload a file, with an optional title, and follow each job through discover, fetch, extract, chunk and index. Failures are listed first. |
| Search preview | Run a query, optionally inside one document and section, and see the ranked results and the raw response an agent receives. |

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

The UI is a Svelte app in `web/`, built into `internal/webui/dist` and
embedded with `go:embed`. A binary built without it answers `/` with a note
to run `mise run web`. `npm run dev` in `web/` serves it with hot reload and
proxies the API to a server on `127.0.0.1:8765`.

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

## Schema migrations

`docsearch migrate` is idempotent and is the only thing that writes the schema
version. Everything else refuses a database it does not match and says what to
run — opening is not upgrading, because every read path opens the database and
one of them running an older build would otherwise stamp a newer database
backwards.

A database *newer* than the build is refused outright rather than downgraded.
An older build cannot know what a newer one changed, so writing through it
risks corrupting data it does not understand.

The version is recorded only after the schema it describes is confirmed
present. A stamp written without that check is a claim that cannot be false,
and the readiness gate would then pass a database whose queries fail.

Run it as a startup precondition of the **worker**, which is the sole writer:

| | |
|---|---|
| systemd | `ExecStartPre=` in `deploy/systemd/docsearch-worker.service` |
| launchd | `migrate && exec worker` in the agent's shell wrapper — launchd has no `ExecStartPre` |
| Kubernetes | an `initContainer` in `deploy/k8s/deployment.yaml` |

Same command everywhere, run before anything serves. A migration that cannot
be applied exits nonzero and the worker does not start. A worker writing
through a schema it does not match is worse than a worker that is down.

Not every schema change can be applied in place. The change from page
references to section references in `index_terms` is not recoverable without
the source documents, so migration refuses it and says to re-ingest. **The
index is fully regenerable**, which is what makes refusing the right answer.

## Running the worker

```bash
docsearch-worker --dsn "$DOCSEARCH_DSN" --root ~/Documents/library
```

One worker serves every library, taking a job from each account in turn, so
one library's long queue cannot starve another and an account created by a
sign-up is served from its first upload. Each job still runs entirely as its
own library's user: the job, the document it produces and the responses it
crawled all belong to one library. `--user` pins a worker to a single
library instead.

Jobs are claimed under a lease. A worker killed mid-job leaves an expired
lease, and the next worker reclaims and restarts the job with no intervention.
Transient failures retry up to three times; deterministic ones (unsupported
format, corrupt file, failed structure validation) fail immediately and say so
rather than burning the budget.

A systemd user unit is in `deploy/systemd/`. Its `TimeoutStopSec=900` exists so
an in-flight job can checkpoint or roll back instead of being killed mid-write.

## Configuration

| | flag | environment |
|---|---|---|
| bind address | `--addr` | `DOCSEARCH_ADDR` |
| database | `--dsn` | `DOCSEARCH_DSN` |
| auth mode | `--auth` | `DOCSEARCH_AUTH` |
| pin the worker to one library | `--user` (worker only) | `DOCSEARCH_USER` |
| library roots | `--root` (repeatable) | `DOCSEARCH_ROOT` (`:`-separated) |
| bearer token | — | `DOCSEARCH_TOKEN` |
| Origin allowlist | `--allowed-origins` | `DOCSEARCH_ALLOWED_ORIGINS` |
| library UI's client at the oidc provider | `--ui-client-id` | `DOCSEARCH_UI_CLIENT_ID` |
| label the library UI shows on every page, such as `Test instance` | `--instance-label` | `DOCSEARCH_INSTANCE_LABEL` |
| permit public bind | `--allow-public-bind` | `DOCSEARCH_ALLOW_PUBLIC_BIND=true` |

`--auth` is `none`, `token`, `oidc` or `password`, and defaults to `token`,
which is what a v1 install already runs. `none` asks for no credential and
answers every request as the single built-in user, so it is refused on any
address that is not loopback, whatever `--allow-public-bind` says.

### Signing in with accounts docsearch holds

`password` mode is for a deployment with no identity provider. docsearch
becomes its own OAuth authorization server: Claude, Claude Code and any other
MCP client discover it, register themselves, and send the person to a
sign-in page. Each account has a library of its own.

```bash
docsearch account add alice                 # asks for a password twice
docsearch account add bob --generate        # makes one up and prints it once
docsearch account add carol --takes-builtin # carol keeps the existing library
docsearch account passwd alice              # reset, and lift any lock
docsearch account unlock alice
docsearch account remove alice              # ends sign-in; the library stays
docsearch account list
```

`docsearch account` connects as the database owner, through `--dsn` or
`DOCSEARCH_DSN`, the same credential `migrate` uses. Whoever can migrate the
schema can manage accounts, and nobody else can.

| | |
|---|---|
| passwords | argon2id at 19 MiB, at least 12 characters; four are checked at a time |
| failed sign-ins | five in a row lock the account for 15 minutes |
| rate limits | per address: 30 registrations an hour, 60 authorizations and 20 sign-ins a minute |
| access tokens | ES256 JWTs, valid for an hour |
| refresh tokens | valid for 30 days, and replaced every time one is used |
| clients | register themselves (RFC 7591), hold no secret, and must use PKCE; one that signs nobody in within a day is cleared |

Set `--public-url` to the address people reach the server at. It is the
issuer every token names, and the sign-in page lives there. A refused sign-in
never says whether the username exists. There is no self-service reset,
since that needs a mail server; `docsearch account passwd` is the reset.
Removing an account stops its tokens at once rather than when they expire.
Behind a reverse proxy, every request has the proxy's address, so the rate
limits apply to the deployment as a whole. A forwarded header is not trusted,
because whoever sends one chooses it.

With accounts, in `password` or `oidc` mode, the server takes no filesystem
paths. Every account would read the same `--root` directories, so anyone
signed in could index any file under them. Accounts add documents by URL,
by upload, or through `docsearch-sync`. To let every account read the roots
anyway, set `--shared-paths` (`DOCSEARCH_SHARED_PATHS`) with `--root`. A
server given roots without it refuses to start.

### Signing in through your own provider

In `oidc` mode your identity provider is the OAuth authorization server, and
docsearch reads the tokens it issues. Each identity gets a library of its
own. Any OIDC provider works; what docsearch needs from it is a token whose
audience names docsearch.

| | flag | environment |
|---|---|---|
| issuer | `--oidc-issuer` | `DOCSEARCH_OIDC_ISSUER` |
| docsearch's `/mcp` URI | `--oidc-resource` | `DOCSEARCH_OIDC_RESOURCE` |
| expected audience, if not the URI | `--oidc-audience` | `DOCSEARCH_OIDC_AUDIENCE` |
| scopes a client should request | `--oidc-scopes-supported` | `DOCSEARCH_OIDC_SCOPES_SUPPORTED` |
| scopes every token must carry | `--oidc-required-scopes` | `DOCSEARCH_OIDC_REQUIRED_SCOPES` |
| client ID, to introspect opaque tokens | `--oidc-client-id` | `DOCSEARCH_OIDC_CLIENT_ID` |
| client secret | — | `DOCSEARCH_OIDC_CLIENT_SECRET` |
| subject who keeps the existing library | `--oidc-default-owner` | `DOCSEARCH_OIDC_DEFAULT_OWNER` |
| create accounts on first sign-in | `--oidc-sign-up` | `DOCSEARCH_OIDC_SIGN_UP` |
| claim every token must carry | `--oidc-required-claim-name` | `DOCSEARCH_OIDC_REQUIRED_CLAIM_NAME` |
| value that claim must hold | `--oidc-required-claim-value` | `DOCSEARCH_OIDC_REQUIRED_CLAIM_VALUE` |

Scope lists are comma-separated. A signed JWT is checked against the keys the
issuer publishes. An opaque token is sent back to the issuer for
introspection, which needs the client ID and secret; without them an opaque
token is refused.

**Who gets a library** follows what Grafana and Gitea do. Restrict who can
sign in to docsearch at the provider first. Sign-up starts closed, so an
identity with no account is refused until you pass `--oidc-sign-up`. A
required claim, such as `groups` holding `docsearch-users`, is checked on
every request, so removing someone from the group at the provider removes
their access here.

**Switching an existing install** keeps its library. Name your own subject
with `--oidc-default-owner`, and your first sign-in takes the library the
deployment already holds. Nobody else can claim it.

**The CLI** sends `DOCSEARCH_TOKEN` as its bearer token in every mode. In
`oidc` mode that is an access token from your provider, and the CLI reads the
library of the identity the token names.

**The library roots are a security boundary.** `add_document` accepts a target
that arrives in a tool call, over a network endpoint, and may have been
suggested by the content of a document rather than typed by a person. A path is
resolved through symlinks and confirmed inside one of the roots; every
rejection returns the same error, so the tool cannot be used to probe for
files.

**A target may also be an `http(s)` URL**, crawled as one document. It is held
to the same standard: scheme, host and every resolved address are validated at
the tool boundary before anything is queued, again in the worker — a job row is
not proof that anything checked it — and again on every redirect hop. Rejections
are likewise indistinguishable, so the tool cannot be used to map an internal
network.

The roots come from server configuration only. No tool parameter can widen
them. More than one may be configured — `--root A --root B`, or
`DOCSEARCH_ROOT=A:B` — and one unusable root fails startup rather than being
dropped, because a server quietly serving three of four configured directories
surfaces the fourth only as an unexplained rejection much later.

**The configured roots are named in `add_document`'s description.** A caller
that can see neither the roots nor why a path was refused cannot act on the
rejection, which left the tool usable only on files a person had already
staged. Which directories an operator chose to expose is not what the uniform
error protects; the caller's path is.

**The server refuses to bind a non-loopback address** without
`--allow-public-bind`, and logs a warning when you pass it.

**Bearer token on every `/mcp` request** in `token` mode, compared in constant time over
SHA-256 digests — and compared even when the header is absent, so a missing
token cannot be distinguished from a wrong one by timing.

**Origin allowlist** as a DNS-rebinding defence. An absent `Origin` is allowed
deliberately: native MCP clients send none, and rejecting that would break
every real client while stopping no browser. A browser Origin not on the list
is rejected. Both branches are pinned by tests.

`GET /healthz` and `GET /readyz` are unauthenticated for probes and disclose
nothing about library contents. `/readyz` fails on a schema-version mismatch,
so a server deployed against a database it was not built for is pulled from
service rather than answering wrongly.

## Registering the server with a client

For a client with native HTTP MCP support:

```json
{
  "mcpServers": {
    "docsearch": {
      "type": "http",
      "url": "https://docsearch.your-tailnet.ts.net/mcp",
      "headers": { "Authorization": "Bearer YOUR_TOKEN" }
    }
  }
}
```

### Claude Desktop

**Claude Desktop (tested on 1.24012.9) does not accept HTTP entries in
`claude_desktop_config.json`.** It logs
`Skipped invalid MCP server config entries: { invalidServers: ['docsearch'] }`
and then `[localMcpBridge] no stdio servers connected`. That file takes stdio
servers only, so the HTTP service is reached through a bridge.

Install `deploy/client/docsearch-server-bridge` to `~/.local/bin/`, then:

```json
{
  "mcpServers": {
    "docsearch": {
      "command": "/Users/YOU/.local/bin/docsearch-server-bridge",
      "args": []
    }
  }
}
```

The bridge reads the token from `~/.config/docsearch/token.env` at launch
rather than taking it from the config file. `claude_desktop_config.json` is the
file most likely to end up in a screenshot or a bug report; the token should
not be in it.

Confirm with `[LocalMcpServerManager] Connected to docsearch (7 tools)` in
`~/Library/Logs/Claude/main.log`.

## Running as a service

**Linux:** `deploy/systemd/` — two user units.

**macOS:** `deploy/launchd/` — two user agents. Install with:

```bash
for f in server worker; do
  sed "s|__HOME__|$HOME|g" deploy/launchd/com.bamsammich.docsearch-$f.plist \
    > ~/Library/LaunchAgents/com.bamsammich.docsearch-$f.plist
  launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.bamsammich.docsearch-$f.plist
done
```

Both set `RunAtLoad` and `KeepAlive`, so they start at login and respawn if
killed. Logs go to `~/Library/Logs/docsearch/`.

launchd has no `EnvironmentFile`, so both agents source the token and the
DSNs from their 0600 file in a shell wrapper. They stay out of the plists and
out of `launchctl print` output. The file holds two DSNs: `DOCSEARCH_DSN` for
the request role, which the server and the worker use, and
`DOCSEARCH_OWNER_DSN` for the owner, which only `docsearch migrate` and
`docsearch account` read. A bearer token must be at least 32 characters;
`openssl rand -base64 48` makes one. The agents expect a Postgres
reachable at that DSN; `deploy/docker/compose.yaml` is the simpler route on
macOS, since it brings its own.

### Docker, on the same machine

`deploy/docker/compose.yaml` runs the same two processes as containers built
from the checkout, and replaces the agents above rather than joining them —
both bind `127.0.0.1:8765` and both write the same database, so run one or the
other.

```bash
scripts/docsearch-up          # rebuild and roll forward
scripts/docsearch-up --logs   # the same, then follow the logs
```

That one command is the whole update story. `up -d --build` rebuilds both
images and recreates a container only when its image digest actually changed,
so running it after a merge that touched neither tree costs a cache-hit build
and leaves the containers alone. Run it after every merge; it is safe to run
when nothing changed. `restart: unless-stopped` plus Docker Desktop's
start-at-login brings the stack back after a reboot.

Two things about this shape are load-bearing:

**The library is bind-mounted at the same absolute path it has on the host,**
and `--root` is that same path. `add_document` takes a filesystem path from the
MCP client, which is a process on the *host*; mounting the library at
`/library` would reject every path a client can actually name.

**Postgres runs as its own service**, on the image that ships `pg_textsearch`
with the library preloaded, and its data lives in a named volume on ext4
inside the Linux VM. `deploy/docker/initdb/00-roles.sql` does the three
things docsearch cannot do for itself: `CREATE EXTENSION` is superuser-only,
`CREATE ROLE` needs `CREATEROLE`, and the database has to exist before
anything can be migrated into it. Postgres runs that file once, on an empty
data directory.

Two roles, because two jobs need different privileges. `docsearch_owner`
owns every table and runs migrations; `docsearch_app` owns nothing and
reaches rows only through the policies, which is what a request runs as.
Neither is a superuser and neither may bypass row-level security, so the
owner is bound by the policies too.

Migration runs in its own service for the reason the systemd unit runs it
from `ExecStartPre`. Both real services gate on
`service_completed_successfully`, so a migration that cannot be applied
means neither ever starts.

No client configuration changes: the container publishes the same
`127.0.0.1:8765` and reads the same token file, so the bridge in
`deploy/client/` keeps working untouched.

## Deployment

`deploy/k8s/` holds one Deployment, `replicas: 1`, `strategy: Recreate`. One
worker serves one library, so a second replica would claim the same queue;
`FOR UPDATE SKIP LOCKED` makes two workers safe, which turns the replica
count into a throughput decision rather than a correctness one.

Server and worker are two containers in one Pod because they share the
library volume, and an RWO claim attaches to one node. Exposed over
Tailscale, never an Ingress.

Postgres is operated by the cluster, with its own storage and its own
backups. `deploy/k8s/config.yaml` carries a placeholder secret holding both
connection strings; a cluster operator that already issues Postgres
credentials, such as CloudNativePG, generates one of its own, and the
Deployment points at that instead. The database needs `pg_textsearch`, which
only a superuser can install: `docs/plans/postgres-multiuser.md` carries the
full privilege boundary.

`deploy/systemd/` has units for running both processes on a single host. The
code does not know which deployment shape it is in.

## Where structure comes from

Chunk boundaries are only as trustworthy as the source they came from, so
sources are ranked by how much of what they assert is declared by the document
rather than inferred from it. A PDF is tried in this order:

| | source | supplies | corroboration |
|---|---|---|---|
| 1 | embedded outline (`get_toc`) | sections, nesting, and the page each begins on | none — nothing it asserts is inferred |
| 2 | printed table of contents | sections and nesting | body headings must agree exactly, or ingest fails |
| 3 | font-size hierarchy | sections, nesting, position | no independent source exists to check it against |
| 4 | none of the above | — | refuse, rather than fall back to fixed windows |

Tier 1 needs no corroboration because there is nothing more reliable to check
it against — the author wrote the bookmarks. Tier 2 is equally author-declared
but recovered by parsing a printed page, so the parse is verified against the
body and a disagreement fails the ingest. Tier 3 is inference all the way down.

**A source that wins also supplies positions.** An outline's page numbers place
its sections directly, with the entry title matched against that page to find
the offset within it. This matters for documents that style headings by weight
or colour rather than size: there is no font hierarchy to detect, and requiring
one would reject a document whose structure is fully declared.

Non-PDF formats sit at tier 1 by construction — a Markdown `##`, an HTML `<h2>`
and a DOCX heading style are all declarations, not inferences.

## Adding a format adapter

One module plus one registry entry. The chunker is untouched.

1. Write `internal/adapter/yourformat`, exporting an `Extract` that returns
   a `domain.Extraction`.
2. Emit `Block`s: `HeadingPath` (full ancestry, root-first), `Locator`
   (a page for paginated formats, an offset otherwise), and `Text`.
   Set `Section` only if the format carries authoritative numbering.
3. Register the suffix in `internal/adapter`.

The chunker reads only the normalized intermediate and never learns which
adapter produced it. If your format's structure cannot be derived, **raise**
rather than falling back to fixed windows — silent degradation to token
windows is how a document quietly becomes unsearchable.

Chunking rules, in one place: the chunk unit is the deepest numbered section a
document declares, falling back to the deepest heading path when there is no
numbering. Units over ~1200 tokens subdivide; units under ~100 tokens merge
forward, but only when the document did not number them. A numbered boundary is
one the document declared, and small numbered sections stay small.

## Where the measured figures come from

Every number in this README was measured on one corpus against one committed
query set. They describe BM25 over that corpus; they are not properties of the
software and not a forecast for your documents.

| | |
|---|---|
| reference corpus | two technical manuals — one paginated and section-numbered (944 chunks), one non-paginated and unnumbered (265) |
| query set | `tests/retrieval/queries.json` — 54 committed, 53 scored |
| baselines | `tests/retrieval/baseline-{A,B,C}*.txt` |
| full detail | [docs/research/retrieval-quality.md](docs/research/retrieval-quality.md) |

A corpus with different vocabulary, structure or size will produce different
numbers. What transfers between corpora is the mechanism behind each result,
not the figure — so each result below states its mechanism first.

## Use the tools in the right order

Cold keyword search is the weakest way to use the index, and every figure here
is measured that way because it is the hardest framing.

**Orient first.** `list_documents` → `outline` → `search` with `section_filter`
set to the chapter that plainly covers the question. Of the 15 queries that
single-shot search missed at k=8, **14 came back at rank 1** once scoped.

**That 14/15 is an upper bound, not a cold-start figure.** The harness chose
each `section_filter` using the query's expected section — an oracle it had and
a real caller does not. A model picking the chapter from the outline alone will
sometimes pick wrong, and the true figure is lower by however often that
happens. It has not been measured.

The conclusion survives the caveat comfortably: those 15 queries score **0/15**
single-shot, so even a substantially degraded real-world recovery rate is a
large gain. What is established is that the answers are present and reachable
once the search is scoped; what is not established is how reliably a model
scopes it correctly.

The mechanism generalizes even where the number does not: scoping a lexical
search to a section shrinks the candidate pool to one where the document's own
vocabulary dominates. That is why `outline`'s tool description says to call it
before searching.

## Measured and rejected

Two plausible retrieval improvements were built, measured, and rejected. Both
are recorded here rather than only in `docs/research/` because these are the
decisions most likely to be re-litigated by someone who has not seen the
numbers.

### Back-of-book index-term boost — too diffuse to discriminate

Boosting chunks whose section a matching back-of-book index term names sounds
free, and on precise lookups it works. It fails on exactly the conceptual
queries it was meant to rescue, because a back-of-book term routinely names
dozens of sections at once.

On the reference corpus the index parsed cleanly — 1,841 references, 100%
resolving to known sections — and the conceptual queries under test matched
**25, 46 and 21 sections** respectively. A boost applied to 46 of 827 sections
is a constant, not a signal. It is still in the code because it costs nothing
and helps precise lookups, but it does not do the job it was added for.

### Dense reranking — actively harmful at the k that matters

`all-MiniLM-L6-v2` over the top 50 BM25 candidates. Scored into top-3 it was
net +3 across 53 queries. Scored into **top-8 — the k a model actually reads —
it is net −2** (2 rescued, 4 displaced), and the conceptual-slang category it
existed to fix nets −1.

**A specialist register defeats a general-purpose embedder.** The model's
priors come from general English, so a corpus whose terms of art collide with
ordinary words gets ranked confidently wrong — and any corpus with a specialist
register (legal, medical, industrial, in-house jargon) should expect the same.
On the reference corpus the collisions are lighting-console terms: "fader",
"look", "cue stack", "executor" and "programmer" all mean something else in
general English, and every query built on them got *worse*:

| query | BM25 rank | reranked |
|---|---|---|
| sub**master** **fader** to hold a **look** | 4 | **12** |
| record a snapshot of the current **look** | 13 | **33** |
| **cue stack** on a **fader** | 6 | **22** |

BM25's ignorance is neutral. The embedder's confidence is wrong, and it is
wrong precisely on the vernacular a semantic model was supposed to help with.

**And it undoes a cheaper fix.** The query that motivated classifying
keyword-reference chunks — q17, "step by step assign a fader to control a group
master" — moved to **rank 1** on that structural change. Reranking pushes it
back to **rank 9**. A cheap, explainable, structural change beat the dense
model on the very case that prompted the investigation.

Verdict: no vector index, no embedding step in ingest, no second recall path.
Reopen only if the corpus changes character or a domain-adapted model appears;
if reopened, the shape stays sqlite-vec as a reranker over the top ~50 BM25
hits, never primary retrieval, with displacement count as a release gate.

## Upgrading from v1

v1 kept its index in a SQLite file, and v2 keeps it in Postgres. Nothing in
the v1 file is copied across: `docsearch import-v1` reads its list of
documents and queues each one again from the file or site it came from, so
v2's own pipeline reads every document. Titles are kept. A document v1 never
finished is skipped and named, so you can add it by hand once you know why it
failed.

1. Stop v1, and copy its index out of the volume it lives in. The import opens
   the copy read-only, so v1's file is untouched and you can go back to it.
2. Start v2 against the same library directory, then run the import:

   ```
   docsearch import-v1 --dry-run docsearch.db
   docsearch import-v1 docsearch.db
   ```

3. Watch the queue with `docsearch jobs` until every job is `ready`.

v1 recorded each file at the path v1's process saw. If v2 sees the library at
a different path, map one to the other with `--root-map OLD=NEW`, repeated
for each root. A source v2 refuses is reported and the rest are still queued,
and the command exits nonzero so a script notices.

## Backups

**The index is fully regenerable from the library volume.** Every chunk,
search row, page and index term is derived from the source documents by
`docsearch ingest`, and the crawl cache holds responses a crawl can fetch
again. Losing the database costs the time to re-ingest, nothing more.

The volume that needs backing up is **`docsearch-library`**, which holds the
only irreplaceable data. `docsearch-data` can be treated as a cache.

## Testing

```bash
go test ./...                                    # every package, and the integration suites
go run ./cmd/docsearch-eval --dsn "$DOCSEARCH_DSN"             # retrieval evaluation
go run ./cmd/docsearch-eval --dsn DSN --self-label             # measure any corpus, no query set
```

`--self-label` builds each query from a chunk's own heading and body and asks
whether that chunk comes back, so it runs on any index the moment it is
ingested. Its limits are severe and it prints them: a self-labelled query
shares terms with its answer by construction, so it cannot see the vocabulary
gap, and a document cut into fixed windows scores 100% because every slice
still holds distinctive words. It catches the failures that leave an index
where nothing is findable — collapsed structure, boilerplate dominating the
term mass, chunk sizes in the wrong unit. Use `verify` to judge structure.

`tests/retrieval/queries.json` is a committed query set with expected sections
recorded before the queries were ever run. **A query that misses stays in the
file unedited** — rewording queries until they pass overfits to a moving test
set. Baselines are committed alongside it so the effect of a change is
measurable.

**recall@8 is the headline metric**, not top-3. The consumer is a model reading
k=8 full chunks, not a person scanning three results.

Cold single-shot search on the reference corpus, by query cohort:

| cohort | @1 | @3 | **@8** | @20 |
|---|---|---|---|---|
| all (n=53) | 42% | 53% | **68%** | 79% |
| heading-term (16) | 62% | 88% | **94%** | 94% |
| body-term (5) | 60% | 60% | **80%** | 80% |
| spans-boundary (6) | 50% | 67% | **83%** | 100% |
| conceptual-slang (20) | 20% | 25% | **45%** | 65% |
| figure-dominated (6) | 33% | 33% | **50%** | 67% |

Add orientation (`outline` → `section_filter`) and 14 of the 15 remaining @8
misses come back at rank 1 — an **upper bound under oracle section selection**,
see above.

Of the 11 queries missing even at @20, 9 share at least one term with their
target section and 2 share none — so most are reachable in principle and the
failure is ranking depth, not absence.

## License

docsearch is released under the [MIT License](LICENSE).

PDFs are read by [PDFium](https://pdfium.googlesource.com/pdfium/), BSD and
Apache licensed, through [go-pdfium](https://github.com/klippa-app/go-pdfium)
in WebAssembly mode, under MIT. v1's worker used PyMuPDF, which is AGPL-3.0;
v2 carries no AGPL component.
