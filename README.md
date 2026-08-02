# docsearch

Local document search over an MCP server. Documents are ingested into one
SQLite database by a background worker; a Go server reads that database and
exposes search, navigation and retrieval tools over MCP Streamable HTTP.

No vector database, no embeddings, no external services. SQLite FTS5 with BM25.
[An embedding reranker was measured and rejected](docs/research/embedding-rerank-probe.md).

## Three processes

| Process | Lifetime | Role |
|---|---|---|
| Ingest worker | daemon | claims queued jobs, extracts, chunks, writes |
| MCP server | daemon | reads the database, enqueues jobs, reports status |
| CLI | one-shot | manual ingest, listing, removal, verification |

Ingest never runs inside the server process. Extraction saturates a core for
minutes and would starve request handling, and the extraction stack is Python
while the server is Go.

## Setup

```bash
mise install          # python 3.13, uv, go 1.26, golangci-lint
uv sync               # python dependencies
go build -o bin/docsearch-mcp ./cmd/docsearch-mcp
```

`pdffonts` (poppler) is useful for inspecting a PDF's text layer and is not in
the mise registry — install it from your package manager.

## Ingesting

```bash
docsearch ingest  <path> [--title T] [--db PATH]   # synchronous, file or directory
docsearch enqueue <path> [--title T] [--db PATH]   # queue for the worker
docsearch worker  [--db PATH] [--root PATH]        # run the daemon
docsearch jobs    [--db PATH]                      # queue state
docsearch list    [--db PATH]
docsearch remove  <doc_id> [--db PATH]
docsearch verify  <doc_id> [--db PATH]
```

`ingest` and the worker call the same function; only the driver differs.

**Run `verify` after every ingest.** It reports chunk count, token
distribution, locator gaps, and the ten longest and shortest chunks with their
heading paths. Structural extraction failures are obvious there and invisible
elsewhere — a collapsed heading source shows up as a handful of enormous
chunks, a shattered one as hundreds of near-empty ones.

A document is invisible to search until its ingest completes. Every read path
filters `documents.status='ready'`.

## Running the worker

```bash
docsearch worker --db var/docsearch.db --root ~/Documents/library
```

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
| database | `--db` | `DOCSEARCH_DB` |
| library root | `--root` | `DOCSEARCH_ROOT` |
| bearer token | — | `DOCSEARCH_TOKEN` |
| Origin allowlist | `--allowed-origins` | `DOCSEARCH_ALLOWED_ORIGINS` |
| permit public bind | `--allow-public-bind` | — |

**The library root is a security boundary.** `add_document` accepts a path that
arrives in a tool call, over a network endpoint, and may have been suggested by
the content of a document rather than typed by a person. Paths are resolved
through symlinks and confirmed inside the root; every rejection returns the
same error, so the tool cannot be used to probe for files.

The root comes from server configuration only. No tool parameter can widen it.

**The server refuses to bind a non-loopback address** without
`--allow-public-bind`, and logs a warning when you pass it.

**Bearer token on every `/mcp` request**, compared in constant time over
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

## Deployment

`deploy/k8s/` — one Deployment, `replicas: 1`, `strategy: Recreate`. SQLite
permits a single writer and the server writes `ingest_jobs`; a rolling update
would briefly run two writers against one file.

Server and worker are two containers in one Pod because they share the database
volume, and an RWO volume attaches to one node. Exposed over Tailscale, never
an Ingress.

The database must sit on a **block-backed** volume with a local filesystem
(Ceph RBD). SQLite locking is unreliable over NFS and CephFS, and WAL needs
shared-memory support they do not provide correctly. This fails by corrupting
under concurrency, not by erroring.

`deploy/systemd/` has units for running both processes on a single host. The
code does not know which deployment shape it is in.

## Adding a format adapter

One module plus one registry entry. The chunker is untouched.

1. Write `python/docsearch/adapters/yourformat.py` exporting
   `extract(path, progress=None) -> Extraction`.
2. Emit `Block`s: `heading_path` (full ancestry, root-first), `locator`
   (`{"page": n}` for paginated formats, `{"offset": n}` otherwise), and `text`.
   Set `section` only if the format carries authoritative numbering.
3. Register the suffix in `adapters/__init__.py`.

The chunker reads only the normalized intermediate and never learns which
adapter produced it. If your format's structure cannot be derived, **raise**
rather than falling back to fixed windows — silent degradation to token
windows is how a document quietly becomes unsearchable.

Chunking rules, in one place: the chunk unit is the deepest numbered section a
document declares, falling back to the deepest heading path when there is no
numbering. Units over ~1200 tokens subdivide; units under ~100 tokens merge
forward, but only when the document did not number them. A numbered boundary is
one the document declared, and small numbered sections stay small.

## Testing

```bash
uv run pytest                                    # ingest, chunker, worker, policy
go test ./...                                    # store, transport, path validation
go run ./cmd/docsearch-eval --db var/docsearch.db  # retrieval evaluation
```

`tests/retrieval/queries.json` is a committed query set with expected sections
recorded before the queries were ever run. **A query that misses stays in the
file unedited** — rewording queries until they pass overfits to a moving test
set. Baselines are committed alongside it so the effect of a change is
measurable.

Current: 42% top-1, 55% top-3 across 53 queries. Strong on heading terms (88%
top-3), weak on conceptual phrasing (30% top-3). Both numbers are honest and
the misses are printed in full.
