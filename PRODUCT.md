# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

The server is Go (`docsearch-server`, v2). The library UI is a small SPA built
with Svelte 5 and Vite, embedded in the server with `go:embed`, talking to the
existing ConnectRPC services through `@connectrpc/connect-web` and to `/mcp`
for search preview. Browser OAuth uses `oidc-client-ts`.

## Users

A person who runs coding agents against their own documentation library. Each
signed-in account manages only its own library; Postgres row-level security
already enforces that boundary. They open the UI occasionally and with a task:
right after queueing a document, or when an agent's search came back thin and
they want to know why.

## Product Purpose

docsearch holds the full text of manuals, specifications and documentation
sites, and serves search, navigation and retrieval to agents over MCP. The
library UI lets a person see what their library holds, add to it, watch ingest,
and check whether each document is actually searchable.

Success: within seconds the person knows which documents are searchable, which
are degraded or failed, and why.

## Positioning

docsearch grades its own index. `verify` returns `good`, `degraded` or
`unusable` per document, with evidence for each defect, because a document can
ingest cleanly, reach `ready`, and still be shaped so retrieval cannot work on
it. No embeddings and no vector database: BM25 over Postgres.

## Operating Context

- Documents are PDFs, files and whole documentation sites. One site is one
  document, its navigation becoming the outline.
- Ingest runs in a separate worker. Jobs move through discover, fetch, extract,
  chunk and index, take minutes, and can stall, fail (sometimes permanently) or
  be cancelled.
- A document is invisible to search until ingest completes.
- The same library is read by agents through MCP tools: `list_documents`,
  `outline`, `search`, `get_context`, `add_document`, `ingest_status`,
  `cancel_ingest`.

## Capabilities and Constraints

- Auth is the MCP's auth. The browser is one more client of the same protected
  resource: in `oidc` mode it runs authorization code + PKCE against the issuer
  named by the protected-resource metadata and sends that bearer; in `token`
  mode it asks for the shared token once per tab; `none` is loopback-only. The
  server holds no sessions and sets no cookies.
- v1 scope: library health, ingest queue, search preview. No operator view
  across accounts.
- Crawls cap at 500 pages by default and obey `robots.txt`.
- Open: re-chunking a site from its crawl cache has no RPC, so the UI offers
  only a full re-ingest; password mode's sign-in comes with that mode.

## Evidence on Hand

No screenshots, customers or usage figures exist. Demonstration content must
be synthetic and labeled as such, and must not name real documents from any
person's library.

## Product Principles

1. Honest state over reassurance. `ready` is not the same as searchable, and
   the UI says so loudly.
2. Show what the agent sees. Search preview returns exactly what MCP returns.
3. One credential. Nothing the web UI does creates a second way in.
4. Quiet until something is wrong. Healthy documents recede; problems lead.

## Accessibility & Inclusion

WCAG 2.2 AA. Status is never carried by color alone.
