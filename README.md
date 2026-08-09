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

Install `deploy/client/docsearch-mcp-bridge` to `~/.local/bin/`, then:

```json
{
  "mcpServers": {
    "docsearch": {
      "command": "/Users/YOU/.local/bin/docsearch-mcp-bridge",
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
for f in mcp worker; do
  sed "s|__HOME__|$HOME|g" deploy/launchd/com.bamsammich.docsearch-$f.plist \
    > ~/Library/LaunchAgents/com.bamsammich.docsearch-$f.plist
  launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.bamsammich.docsearch-$f.plist
done
```

Both set `RunAtLoad` and `KeepAlive`, so they start at login and respawn if
killed. Logs go to `~/Library/Logs/docsearch/`.

launchd has no `EnvironmentFile`, so the server agent sources the token from
its 0600 file in a shell wrapper — it stays out of the plist and out of
`launchctl print` output.

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

## Backups

**The SQLite index is fully regenerable from the library volume.** Every chunk,
FTS row, page and index term is derived from the source documents by
`docsearch ingest`. Losing `docsearch.db` costs the time to re-ingest, nothing
more.

The volume that needs backing up is **`docsearch-library`**, which holds the
only irreplaceable data. `docsearch-data` can be treated as a cache.

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
