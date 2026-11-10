<div align="center">

# docsearch

**Full-text search over your manuals and documentation sites, served to AI agents over MCP**

Ingest PDFs, Word files, Markdown and whole documentation sites, and your agent can outline, search and cite them. Ranking is BM25 in Postgres through `pg_textsearch`, with no embeddings, vector database or outside service.

</div>

On five documentation sites, one cold `search` call put the expected page in its top 8 results for 65 of 73 queries. Results come back as whole chunks with a page number, or a URL and anchor, so your agent can cite where each answer came from. `docsearch verify` grades each document `good`, `degraded` or `unusable`, so you find out when a PDF ingested without errors yet came out in a shape that search cannot use.

## Run docsearch with Docker

You need Docker with Compose, `openssl` and `curl`. Skip `go install`, because the module path has no `/v2` suffix and `@latest` still fetches v1.

```bash
# 1. Clone, then write a bearer token to the 0600 file the compose stack reads
git clone https://github.com/bamsammich/docsearch && cd docsearch
mkdir -p ~/.config/docsearch ~/Documents/docsearch-library
(umask 077; printf 'DOCSEARCH_TOKEN=%s\n' "$(openssl rand -base64 48)" > ~/.config/docsearch/token.env)

# 2. Build every image from this checkout, then start Postgres, schema migration, server and ingest worker
scripts/docsearch-up

# 3. Prints "ready" once the server is up; if curl cannot connect, wait a few seconds and run curl again
curl http://127.0.0.1:8765/readyz

# 4. Load the token into this shell and upload a first document; the worker indexes the file in the background
. ~/.config/docsearch/token.env
curl -T docs/configuration.md -H "Authorization: Bearer $DOCSEARCH_TOKEN" \
  'http://127.0.0.1:8765/upload?name=configuration.md'

# 5. Print the MCP client entry for this server, with your token filled in
printf '{"mcpServers":{"docsearch":{"type":"http","url":"http://127.0.0.1:8765/mcp","headers":{"Authorization":"Bearer %s"}}}}\n' "$DOCSEARCH_TOKEN"
```

The stack serves the library at `~/Documents/docsearch-library`, and `DOCSEARCH_LIBRARY` points the stack at a different folder. Port 8765 is published on loopback only. Run `scripts/docsearch-up` again after every pull, and Compose recreates only the containers whose image changed.

The library UI is at `http://127.0.0.1:8765/`. Sign in with the token from `~/.config/docsearch/token.env`.

## Connect an MCP client

Paste the entry from step 5 into any client with native HTTP support. To test the connection, ask the agent to use docsearch to find how to sign in through an OIDC provider. The answer comes from `docs/configuration.md`, the file uploaded in step 4.

Claude Desktop (tested on 1.24012.9) accepts only stdio servers in `claude_desktop_config.json`, so connect Claude Desktop through the bridge in `deploy/client/`. The bridge runs `mcp-remote` with `npx` and needs Node.

```bash
mkdir -p ~/.local/bin && install -m 755 deploy/client/docsearch-mcp-bridge ~/.local/bin/
```

```json
{"mcpServers":{"docsearch":{"command":"/Users/YOU/.local/bin/docsearch-mcp-bridge","args":[]}}}
```

Write the full path to your home directory in place of `/Users/YOU`. The bridge reads the token from `~/.config/docsearch/token.env` each time Claude Desktop starts the bridge, so the token never goes in `claude_desktop_config.json`.

## Add documents from a shell

The `curl` upload in step 4 needs nothing else installed. For files already in the library folder, and for documentation sites, build the `docsearch` CLI. You need Go 1.26 on your path, and [Contributing](CONTRIBUTING.md) has the mise setup for the pinned toolchain.

```bash
go build -o bin/docsearch ./cmd/docsearch
. ~/.config/docsearch/token.env
bin/docsearch enqueue ~/Documents/docsearch-library/manual.pdf
bin/docsearch enqueue https://docs.pytest.org/en/stable/
bin/docsearch jobs --all
```

A file path has to sit under the library folder. For a URL, docsearch crawls the whole site into one document and takes the outline from the site navigation. The crawler honors `robots.txt` and follows links for at most 500 pages by default, and `docsearch refresh` downloads only the pages that changed.

docsearch reads `.pdf`, `.docx`, `.md`, `.markdown`, `.html`, `.htm`, `.txt` and `.text` files. A scanned PDF needs OCR first, and `docsearch inspect` warns you before you ingest one. [Ingesting documents](docs/ingesting.md) has the other CLI commands and the optional Paperless-ngx sync.

## MCP tools your agent gets

| your agent needs to | tools |
|---|---|
| see what the library holds | `list_documents` lists documents, and `outline` lists one document's sections |
| find and cite a passage | `search` ranks with BM25, weights headings above body text and returns the whole chunk with its page, or its URL and anchor; `section_filter` limits a search to one section |
| read past a chunk boundary | `get_context` |
| add a document from the chat | `add_document` takes a path under a library root or an `http(s)` URL, and `upload_link` returns a single-use link for any other file |
| follow ingest | `ingest_status`, `cancel_ingest` |

For a team, docsearch supports password or OIDC accounts with shared libraries and groups, and Postgres row-level security enforces which library each account can read. [Sharing libraries](docs/sharing.md) has roles and groups.

## Retrieval measured on nine sites and two manuals

Each figure is recall@8 from one cold `search` call: the share of queries whose expected page or section appears among the first 8 results. An agent reads 8 whole chunks per search, so the first 8 results are the cut that matters. `cmd/docsearch-eval` scored the site queries through the same search path the MCP server uses.

| corpus | scored queries | recall@8 | write-up |
|---|---|---|---|
| 5 documentation sites: pytest, Sphinx, moonrepo, mdBook, Mintlify | 73, of which 57 blind | 89%, blind only 88% | [site baseline](docs/research/site-retrieval-baseline.md#baseline-on-main-32f96a9) |
| 4 sites chosen for known crawl failures: Next.js, GitBook developers, Fireblocks, SST | 54, all blind | 42 of 54 | [second round](docs/research/site-retrieval-baseline.md#the-fixes-on-the-sites-they-target) |
| 2 technical manuals, 944 and 265 chunks | 53 | 68% | [manuals write-up](docs/research/retrieval-quality.md) |

The blind site queries were written and hashed before anyone crawled their sites, and a query that misses stays in [`tests/retrieval/sites.json`](tests/retrieval/sites.json) unedited. Some blind `expect` values were later corrected, either to the title the site uses or down to a single page, with each change recorded in the query's note. No query text changed. The 16 informed queries, written with the index's heading paths in view, are reported separately.

Conceptual-slang queries, worded in terms absent from the target page, score lowest at 74% on the five sites and 45% on the manuals. On the manuals, calling `outline` and then `search` with a `section_filter` recovered 14 of the 15 queries that single-shot search missed at @8, each at rank 1. Treat 14 of 15 as an upper bound, because the harness picked each filter from the query's expected section, which a real caller does not know. Nobody has measured how often a model picks the right section on its own.

Reranking the first 50 BM25 hits with `all-MiniLM-L6-v2` gained a net 3 queries at @3 and lost a net 2 at @8 (2 rescued, 4 displaced), compared with the unreranked results. On the manuals, terms of art such as "fader" and "cue stack" mean something else in everyday English, and a general-purpose model ranks them by the everyday meaning ([probe](docs/research/embedding-rerank-probe.md)).

Searching every document at once (unscoped, k = 8) takes 257 ms on a synthetic library of 500 documents with 20 to 120 chunks each. The figure is the median of ten three-word queries run five times each, with a second account's partition present ([measurement](docs/research/unscoped-search-cost.md)).

Read the figures as BM25 on those corpora, not as a forecast for your documents. Two more sites, kubernetes.io/docs/concepts and gitbook.com/docs, failed the crawl's completeness gate, so their 25 blind queries are held out instead of scored as misses. A re-crawl can move a site baseline with no code change, so compare a build against a same-day crawl by following [Reproducing](docs/research/site-retrieval-baseline.md#reproducing). The manuals' query set, `tests/retrieval/queries.json`, is missing from the repository, so nobody can rerun the 68% figure from a checkout yet. [Retrieval quality](docs/retrieval.md) has the manuals' per-category table and the rejected ideas in full.

## Server settings

`docsearch-server` reads settings in priority order, highest first: command line, environment, a `docsearch.yaml` named by `--config` (`DOCSEARCH_CONFIG`), values an administrator saved in the library UI, and built-in defaults. Secrets stay out of `docsearch.yaml`. The token and the OIDC client secret come only from the environment, and the DSN comes from `--dsn` or the environment.

| flag | environment | default | sets |
|---|---|---|---|
| | `DOCSEARCH_TOKEN` | | bearer token in `token` mode; at least 32 characters |
| `--dsn` | `DOCSEARCH_DSN` | | Postgres DSN of the request role the server and worker run as |
| | `DOCSEARCH_OWNER_DSN` | | owner DSN that `docsearch migrate` and `docsearch account` read first |
| | `DOCSEARCH_ADMIN_USERNAME`, `DOCSEARCH_ADMIN_PASSWORD` | | first administrator for `password` mode, created by `docsearch migrate` when the username is free; an existing account is left alone. Set both or neither; the password needs at least 12 characters |
| `--root` | `DOCSEARCH_ROOT` | | library directories `add_document` accepts paths under; repeat the flag, or separate with `:` |
| `--auth` | `DOCSEARCH_AUTH` | `token` | `none`, `token`, `password` or `oidc` |
| `--addr` | `DOCSEARCH_ADDR` | `127.0.0.1:8765` | listen address |
| `--allow-public-bind` | `DOCSEARCH_ALLOW_PUBLIC_BIND` | off | permits a non-loopback address |
| `--allowed-origins` | `DOCSEARCH_ALLOWED_ORIGINS` | | comma-separated browser origins; native MCP clients send none |
| `--public-url` | `DOCSEARCH_PUBLIC_URL` | loopback on the listen port; in `oidc` mode the origin of `--oidc-resource` | where a browser reaches the server, for upload links and sign-in |
| `--upload-max-mb` | `DOCSEARCH_UPLOAD_MAX_MB` | `100` | largest upload accepted |

`docsearch-server` exits at startup on any of these unsafe combinations:

| when | the server needs |
|---|---|
| `--auth none` or `--auth token` | at least one `--root` |
| `--auth none` | a loopback `--addr` |
| any other non-loopback `--addr` | `--allow-public-bind` |
| `--root` with `--auth password` or `--auth oidc` | `--shared-paths`, which lets every account read the root |

The `docsearch` CLI finds the server through `--server` (`DOCSEARCH_SERVER`, default `http://127.0.0.1:8765`) and sends `DOCSEARCH_TOKEN`. [Configuration](docs/configuration.md) has the `docsearch.yaml` keys, account setup, OIDC sign-in and the security boundaries.

Without Docker, docsearch needs Postgres with the `pg_textsearch` extension and the two roles defined in `deploy/docker/initdb/00-roles.sql`. `CREATE EXTENSION` takes a superuser. After that, `docsearch migrate` builds the schema with the owner DSN. [Deployment](docs/deployment.md) has the systemd and launchd setup.

## Operator guides

| guide | contents |
|---|---|
| [Ingesting documents](docs/ingesting.md) | the `docsearch` CLI, `inspect` and `verify`, uploads, site crawling, Paperless-ngx sync, where structure comes from |
| [Sharing libraries](docs/sharing.md) | shared libraries, roles, groups, the library web UI |
| [Configuration](docs/configuration.md) | every setting, password and OIDC sign-in, registering clients |
| [Deployment](docs/deployment.md) | systemd, launchd, Docker, Kubernetes, migrations, backups, upgrading from v1 |
| [Retrieval quality](docs/retrieval.md) | the measured figures in full, and what was tried and rejected |
| [Contributing](CONTRIBUTING.md) | building from source with mise, tests, adding a format adapter |

docsearch v2 is a Go rewrite on Postgres. v1, on SQLite, stays on the [`v1` branch](https://github.com/bamsammich/docsearch/tree/v1) and gets security fixes only, until six months after `v2.0.0`.

## MIT license and PDFium

docsearch is released under the [MIT License](LICENSE) and reads PDFs with [PDFium](https://pdfium.googlesource.com/pdfium/) (BSD and Apache) through [go-pdfium](https://github.com/klippa-app/go-pdfium) (MIT) in WebAssembly mode. v1's worker used PyMuPDF, an AGPL-3.0 library that v2 dropped.
