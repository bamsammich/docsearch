# Site-as-document ingest

`docsearch add https://docs.example.com` navigates the site, chunks every page
into the database, and leaves an index that answers offline. One site is one
document, the site's own navigation is the outline, and every gate that grades
a manual grades a site.

## The model

A documentation site is a book whose chapters are pages. Its navigation
declares which pages exist, how they nest, and in what order — the same
information a PDF's embedded outline declares, recovered by parsing, which
places it beside `front_toc` rather than beside `outline`: author-declared,
read through a parser that can misread, checked against what was actually
fetched, and a disagreement fails.

Each page becomes an **authoritative section** keyed by its position in the
nav tree as a dotted numeral, with the page's own `h1`–`h6` nesting beneath
it. A page at the third top-level nav entry, second child, is section `3.2`;
its `##` headings subdivide into `3.2` chunks carrying deeper heading paths.

This is not a new chunking strategy. It is the existing one, fed a
`Block.section` it already knows how to use:

- `_merge_small` never merges across an authoritative boundary, so a stub page
  under `MIN_TOKENS` keeps its own heading path instead of being absorbed into
  the next page and truncated to its parent. Without page-as-section this is a
  silent content-attribution bug, and doc sites are full of short pages.
- `_split_oversized` subdivides a long page at its unnumbered subheadings —
  exactly the behaviour a long chapter already gets.
- `SectionMatchSQL` (`section = ? OR section LIKE ? || '.%'`) filters a nav
  subtree with no query changes.
- `scattered_sections` detects a page whose chunks landed non-contiguously.

The chunker is untouched. The work is acquisition, and producing blocks.

## One source, two schemes

`ingest_file(conn, path)` hard-codes a file: it hashes that file, keys
replacement on its path, and picks an adapter by suffix. None of those hold
for a URL. Rather than growing a site-shaped sibling beside it, both become a
`Source` with four verbs.

| verb | `path` | `url` |
|---|---|---|
| guard | `libroot` — inside a configured root | `urlguard` — scheme, host, resolution |
| acquire | read from disk | fetch, or a cache hit |
| identity | absolute path | canonical base URL |
| extract | adapter by suffix | walk the nav, adapter per page |

`ingest_source(conn, source)` replaces `ingest_file`, the worker dispatches on
`documents.source_kind`, and one transaction boundary, one progress model and
one set of quality gates serve both. `ingest_file` has exactly two callers, so
both migrate in the same change and no compatibility shim stays open.

What the path scheme gains: its guard reports through the same channel the URL
guard does, and `inspect` and `ingest` take the same shape whichever scheme is
passed. Directory ingest keeps producing one document per file — the site
model applies to a crawled site, not to any directory that happens to hold
Markdown.

### Library roots

`DOCSEARCH_ROOT` becomes a list. The rule that no tool parameter can widen the
root is unchanged: the operator configures which directories are ingestable,
and `add_document` still rejects everything else with one indistinguishable
error.

What changes is that the server discloses its configured roots in tool
metadata. A caller that can see neither the roots nor the reason a path was
refused has no recovery from the uniform error, which is why `add_document`
works today only on files a person already staged. Disclosing an operator-set
configuration value is not the filesystem probe the uniform error exists to
prevent.

## Where structure comes from over HTTP

There is no `mkdocs.yml` on the far end of a URL. The manifest arrives
**rendered, as the sidebar** — which makes it simultaneously the chrome to
strip from every page and the outline to derive the document from. One parse
serves both.

Structure sources, in preference order:

| source | what it is | tier |
|---|---|---|
| `nav_sidebar_dom` | the rendered navigation tree, cross-checked against the sitemap | declared, recovered by parsing |
| `nav_llms_txt` | a curated page list carrying hierarchy | declared, recovered by parsing |
| `nav_sitemap` | the sitemap's page set, hierarchy from URL path depth | partly inferred |
| `nav_url_path` | URL path depth alone | inferred, nothing to check it against |

The first two validate. `nav_sitemap` and `nav_url_path` warn, the way
`font_heuristic` warns: structure was inferred and no second source exists to
corroborate it. The addressability gate is what catches them when the guess
was wrong.

## The frontier is the nav, not link-following

A general crawler that follows every link has to be fenced in after the fact.
A documentation site already publishes the list of pages that constitute it,
twice — in its navigation and in its sitemap. The frontier is the union of
those two, scoped to the seed's host and path prefix.

Breadth-first link-following exists only as the last resort when neither
yields anything, and then under an explicit depth and page budget. This is
both more correct — it fetches the documentation rather than the marketing
site and the blog — and it removes an entire risk class rather than bounding
it.

## Completeness is a gate, not a statistic

Three sets exist by the end of a crawl: pages the nav declares, pages the
sitemap declares, and pages actually fetched. Their disagreements mean
different things.

**Declared by the nav, never fetched.** A broken link on a large site is
ordinary and must not refuse the ingest; a large share of them means the site
was not navigable and the index is a silent partial. So: every unreachable
page is reported, and past a share threshold the ingest fails. An index that
covers 60% of a site while reporting success is the web's version of the
unaddressable failure — legally formed, internally consistent, and quietly
wrong.

**In the sitemap, absent from the nav.** Tag pages, changelog archives,
redirects. These carry no position in the nav tree and therefore no section
number, so they are excluded from the document and reported.

**In the nav, absent from the sitemap.** Sitemaps are routinely incomplete.
Not a finding.

## Acquisition

**Politeness.** `robots.txt` honored, with an explicit override flag for sites
the operator runs. Default two concurrent requests and a request interval;
`Retry-After` and `429` respected with backoff; a `User-Agent` that identifies
the tool and its purpose; a page budget that caps a misconfigured seed.

**Redirects.** Bounded hop count. Every hop re-validated by the URL guard —
validating only the seed leaves the guard trivially bypassable. A hop that
leaves the host or the path prefix leaves scope and is dropped, reported.

**Canonical URLs.** `<link rel="canonical">` collapses the duplicates that
versioned documentation produces when a seed spans `latest/` and `v2.1/`.
Path-prefix scoping handles most of it; canonical handles the rest.

**Client-rendered pages.** Detected, named, and reported rather than ingested
as empty shells: a page whose body yields under a floor of extractable text
while carrying substantial script mass is reported by `inspect` with that
reason. Headless rendering is a follow-up behind a flag, not a dependency in
the critical path — the major generators (Docusaurus, MkDocs Material, Sphinx,
mdBook, Nextra, Starlight) all pre-render.

## The fetch cache is a separate database

`var/fetch-cache.db`, not the search database. Different lifecycle and
different reader: the search database is read by the Go server on every
request and is the artifact shipped for offline use; the cache is worker-only,
disposable, and holds raw HTML that would otherwise bloat it permanently.

Keyed on URL, holding final URL, status, content type, `ETag`,
`Last-Modified`, body, fetch time and digest. What it buys:

- Crawl and ingest become separable. A chunker change re-indexes the whole
  site with zero refetch.
- Conditional `GET` on refresh — a re-crawl transfers only what changed.
- A cancelled or crashed crawl resumes instead of restarting.
- When extraction goes wrong, what was actually fetched is inspectable.

## Security boundary

The seed URL arrives through the MCP `add_document` tool, over a network
endpoint, and may have been suggested by the content of a document or a web
page rather than typed by a person. It is held to the standard `libroot` sets.

- Scheme restricted to `http`/`https`.
- Host rejected when it resolves to loopback, private ranges, link-local
  (`169.254.0.0/16` and the IPv6 equivalents), or `.local`.
- Resolution and connection agree: resolve once and connect to that address,
  so a name that passes validation cannot resolve to something else on the
  next lookup.
- Enforced at three points, because none of them can assume another ran: the
  Go tool boundary, the Python worker (a job row is not proof of validation),
  and every redirect hop inside the fetcher.
- Every rejection returns one indistinguishable error. Distinguishing "outside
  scope" from "does not resolve" turns the tool into a network probe.

Targets are public, so the fetcher sends no credentials and there is no
cross-host credential leak to defend against.

## Job model

A crawl runs for minutes. The existing lease renews on every progress write,
which already covers it. Phases become `discover`, `fetch`, `extract`,
`chunk`, `index`; during `fetch`, progress is pages fetched over pages known,
and the count rises as discovery proceeds. Cancellation is checked between
pages.

## Pull requests

Ordered by blast radius ascending. The first three are disjoint and land in
parallel.

| # | scope | touches | after |
|---|---|---|---|
| 1 | Preserve newlines in `<pre>`; exempt it from the parent-tag dedup that drops a `<pre>` inside an `<li>` | `adapters/html.py` | — |
| 2 | `documents.source_kind`, `chunks.url`, `chunks.anchor`; schema 4→5 both sides | `db.py`, `store/*.go`, `mcpserver` | — |
| 3 | Library roots become a list; roots disclosed in tool metadata | `config`, `libroot`, `mcpserver` | 2 |
| 4 | `urlguard`: scheme, host, resolution, redirect re-validation, uniform error | new, Go + Python | — |
| 5 | Fetch cache database and the fetcher: conditional GET, rate limit, robots, redirect handling | new `fetch.py`, cache schema | 4 |
| 6 | Discovery: `robots.txt` sitemap directive, `sitemap.xml` including index files, `llms.txt`, seed fetch | `discover.py` (new) | 5 |
| 7 | Nav tree from the rendered sidebar; chrome identification falls out of the same parse | `nav.py` (new) | 5 |
| 8 | Crawl orchestration: frontier, scope, budget, resume, progress, cancellation | `crawl.py` (new) | 6, 7 |
| 9 | The `Source` abstraction and the ingest refactor: `ingest_source` replaces `ingest_file`, both schemes behind it; sections from nav position, three-way cross-validation, completeness gate | `site.py` (new), `ingest.py`, `cli.py`, `worker.py`, `structure.py` | 2, 8 |
| 10 | Cross-page chrome removal | `site.py` | 9 |
| 11 | `inspect` site mode: live dry run against a URL | `inspect.py` | 8 |
| 12 | Threshold recalibration against a doc-site corpus | `structure.py`, `verify.py` | 9 |
| 13 | `refresh`: conditional re-crawl of an ingested site | `crawl.py`, `cli.py` | 9 |

PR 9 is the only change touching `ingest.py`, `cli.py` and `worker.py`
together, so everything that would collide with it is sequenced behind it. PR 3
follows PR 2 for file overlap in `mcpserver` rather than for any logical
dependency.

New dependency: `httpx`. Parsing reuses the `lxml` and `beautifulsoup4`
already present.

## Verification

Tests are black-box and never touch the network. Fixture sites are served from
a local HTTP server on an ephemeral port, which gives real semantics —
redirects, `304`, `Retry-After`, malformed sitemaps, broken nav links —
without an external dependency. Fixtures cover each generator's sidebar shape.

End to end, before any of this is called done:

1. `docsearch inspect <url>` reports the discovered page count, the structure
   source it would use, the nav/sitemap disagreement, and any client-rendered
   pages, without writing anything.
2. `docsearch add <url>` with no other flags crawls, extracts, chunks and
   indexes, and reports what it found.
3. A second `docsearch add` of the same URL transfers almost nothing and
   reports `unchanged`.
4. `docsearch verify` grades the site, with PR 11's constants in place.
5. `docsearch-eval --self-label` shows retrieval functioning on the index.
6. `search` with a `section_filter` of a nav subtree returns only that
   subtree, and results carry a URL that resolves.
7. The database answers with the network disconnected.

## Deferred

**Headless rendering** for client-rendered sites, behind an explicit flag.
`inspect` names them in the meantime, so the gap is visible rather than silent.

**Authenticated sites.** Targets are public. Adding them later means
credential storage, redaction in logs and errors, and a rule that credentials
never survive a cross-host redirect.

**Incremental re-chunk.** `refresh` refetches only what changed, then
re-chunks the whole site. Correct, and wasteful on a large site whose single
page moved.

**Content upload.** Bytes over the wire rather than a path, which is what a
client that does not share the server's filesystem would need. Out of scope
while the server and its callers are on one machine; the `k8s` manifests are
understood to serve a client that stages files into a mounted root. Adding it
means a staging area with a quota, size caps, content sniffing rather than a
declared type, decompression limits for the zip-container formats, and
digest-keyed replacement — an upload has no `source_path` to key on. The
`Source` abstraction is where it would attach.

## Open

**Corpus for PR 11.** `ADDRESSABLE_MIN = 0.50`,
`ADDRESSABILITY_MIN_CHUNKS = 25`, `HEADLESS_DEGRADED_RATE = 0.02` and the
`verify` grading rates were measured on four manual corpora. Doc sites are
shorter-paged, shallower and code-heavy. Three to five real sites are needed,
spanning generators and including at least one API-reference-heavy site, whose
near-identical method stubs are the shape `classify_kinds` already detects.

**Unreachable-page threshold.** The share of nav-declared pages that may fail
to fetch before the ingest is refused rather than reported. Set it from the
corpus rather than picking a number now.
