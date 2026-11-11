<div align="center">

# docsearch

**Full-text search over your manuals and documentation sites, served to AI agents over MCP**

Ingest PDFs, Word files, Markdown and whole documentation sites, and your agent can outline, search and cite them. Ranking is BM25 in Postgres through `pg_textsearch`, with no embeddings, vector database or outside service.

</div>

On five documentation sites, one cold `search` call put the expected page in its top 8 results for 65 of 73 queries. Results come back as whole chunks with a page number, or a URL and anchor, so your agent can cite where each answer came from. `docsearch verify` grades each document `good`, `degraded` or `unusable`, so you find out when a PDF ingested without errors yet came out in a shape that search cannot use.

## Run docsearch with Docker

You need Docker with Compose 2.23 or later and `openssl`. Nothing gets cloned or built, because the compose file pulls published images from GHCR.

```bash
mkdir docsearch && cd docsearch
curl -fsSLO https://raw.githubusercontent.com/bamsammich/docsearch/main/compose.yaml
echo "DOCSEARCH_TOKEN=$(openssl rand -hex 32)" > .env
docker compose up -d
```

Open <http://127.0.0.1:8765/> and sign in with the token from `.env`. Under Ingest, drop in a PDF, `.docx` or Markdown file, and the worker indexes it in the background. The server listens on loopback only.

To update, run `docker compose pull && docker compose up -d` in the same folder. The images track the newest 2.x release, and `DOCSEARCH_VERSION` in `.env` pins an exact one. The header of [`compose.yaml`](compose.yaml) lists the other settings, and [Deployment](docs/deployment.md) covers building from a checkout instead.

## Connect your agent

Each client needs the token from `.env`. Run `. ./.env` in the docsearch folder first, and the commands that read `$DOCSEARCH_TOKEN` pick it up. To test the connection, ask the agent to list the documents in docsearch.

### Claude Code

```bash
claude mcp add --transport http --scope user docsearch http://127.0.0.1:8765/mcp \
  --header "Authorization: Bearer $DOCSEARCH_TOKEN"
```

`--scope user` makes docsearch available in every project. `claude mcp list` shows whether it connected.

### Claude Desktop

Claude Desktop reads only stdio servers from its config file, so `mcp-remote` bridges to the HTTP endpoint. The bridge needs Node. Open Settings, then Developer, then Edit Config, and add:

```json
{
  "mcpServers": {
    "docsearch": {
      "command": "npx",
      "args": ["-y", "mcp-remote@0.14.3", "http://127.0.0.1:8765/mcp",
               "--header", "Authorization:${AUTH_HEADER}", "--transport", "http-only"],
      "env": { "AUTH_HEADER": "Bearer YOUR_TOKEN" }
    }
  }
}
```

Replace `YOUR_TOKEN` and restart Claude Desktop. To keep the token out of `claude_desktop_config.json`, use the bridge script in [Configuration](docs/configuration.md#claude-desktop).

<details>
<summary><b>Cursor</b></summary>

Add to `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "docsearch": {
      "url": "http://127.0.0.1:8765/mcp",
      "headers": { "Authorization": "Bearer YOUR_TOKEN" }
    }
  }
}
```

</details>

<details>
<summary><b>VS Code</b></summary>

Run **MCP: Open User Configuration** and add this entry. VS Code asks for the token the first time the server starts and keeps it in its secret storage.

```json
{
  "inputs": [
    { "type": "promptString", "id": "docsearch-token", "description": "docsearch token", "password": true }
  ],
  "servers": {
    "docsearch": {
      "type": "http",
      "url": "http://127.0.0.1:8765/mcp",
      "headers": { "Authorization": "Bearer ${input:docsearch-token}" }
    }
  }
}
```

</details>

<details>
<summary><b>Codex</b></summary>

```bash
codex mcp add docsearch --url http://127.0.0.1:8765/mcp --bearer-token-env-var DOCSEARCH_TOKEN
```

The config stores only the variable's name, so export `DOCSEARCH_TOKEN` in the shell that starts Codex.

</details>

<details>
<summary><b>Gemini CLI</b></summary>

```bash
gemini mcp add --scope user --transport http docsearch http://127.0.0.1:8765/mcp \
  --header "Authorization: Bearer $DOCSEARCH_TOKEN"
```

</details>

<details>
<summary><b>Windsurf</b></summary>

Add to `~/.codeium/windsurf/mcp_config.json`:

```json
{
  "mcpServers": {
    "docsearch": {
      "serverUrl": "http://127.0.0.1:8765/mcp",
      "headers": { "Authorization": "Bearer YOUR_TOKEN" }
    }
  }
}
```

</details>

<details>
<summary><b>Any other client</b></summary>

docsearch speaks streamable HTTP at `http://127.0.0.1:8765/mcp`, with the header `Authorization: Bearer YOUR_TOKEN`. A client that supports only stdio can reach it through `mcp-remote`, as in the Claude Desktop entry.

</details>

claude.ai, ChatGPT and other hosted apps connect from the provider's servers, so they cannot reach a loopback address. They need docsearch at a public HTTPS URL with accounts turned on, which [Deployment](docs/deployment.md) and [Configuration](docs/configuration.md) cover.

## Add documents from a shell

The library UI and your agent's `add_document` tool both add documents with nothing else installed. The `docsearch` CLI ships in the worker image, so it can queue a documentation site or a file from the library folder through `docker compose exec`:

```bash
. ./.env
docker compose exec -e DOCSEARCH_TOKEN worker docsearch enqueue https://docs.pytest.org/en/stable/
docker compose exec -e DOCSEARCH_TOKEN worker docsearch jobs --all
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
| `--judge-url` | `DOCSEARCH_JUDGE_URL` | | an optional service that docsearch asks which heading candidates in a PDF are not headings; see [Heading judge](docs/judge.md) |

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
| [Heading judge](docs/judge.md) | the optional service that checks a PDF's inferred headings, and the contract it answers |
| [Retrieval quality](docs/retrieval.md) | the measured figures in full, and what was tried and rejected |
| [Contributing](CONTRIBUTING.md) | building from source with mise, tests, adding a format adapter |

docsearch v2 is a Go rewrite on Postgres. v1, on SQLite, stays on the [`v1` branch](https://github.com/bamsammich/docsearch/tree/v1) and gets security fixes only, until six months after `v2.0.0`.

## MIT license and PDFium

docsearch is released under the [MIT License](LICENSE) and reads PDFs with [PDFium](https://pdfium.googlesource.com/pdfium/) (BSD and Apache) through [go-pdfium](https://github.com/klippa-app/go-pdfium) (MIT) in WebAssembly mode. v1's worker used PyMuPDF, an AGPL-3.0 library that v2 dropped.
