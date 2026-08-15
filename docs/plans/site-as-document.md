# Site-as-document ingest

`docsearch add https://docs.example.com` navigates the site, chunks every page
into the database, and leaves an index that answers offline. One site is one
document, the site's own navigation is the outline, and every gate that grades
a manual grades a site.

## The model

A documentation site is a book whose chapters are pages. Something on the site
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

Directory ingest keeps producing one document per file. The site model applies
to a crawled site, not to any directory that happens to hold Markdown.

### Library roots

`DOCSEARCH_ROOT` is a list, and the server names the configured roots in
`add_document`'s description so a caller can stage a file somewhere acceptable
rather than guessing into a uniform error.

Paths outside every root are refused, and that refusal is the boundary rather
than a default to be softened. No MCP mechanism lets a server *require* a
human decision: elicitation is client-discretionary and absent from some
clients, tool annotations are explicitly hints rather than enforcement, and a
host's confirmation dialog is a SHOULD the operator can switch off. Hard
guarantees have to come from deterministic controls, which is what the roots
are. Any approval flow for out-of-root paths therefore belongs out of band —
an operator action in a context the model cannot reach — and never as a
protocol feature the boundary depends on.

## Coverage and hierarchy are different questions

**They come from different sources, and neither source answers both.** Two
real targets, probed before this was built:

| | `resolume.com/support` | `moonrepo.dev/docs` |
|---|---|---|
| `robots.txt` | `Disallow:` (all allowed) | absent |
| `sitemap.xml` | absent | 600 URLs, 210 under `/docs` |
| `llms.txt` | absent | 286 links, 169 under `/docs` |
| server-rendered | yes | yes (Docusaurus) |
| doc sidebar in the HTML | none — only a marketing nav | present, **19 of 210 pages** |
| pages in scope | 80 | 210 |

A rendered sidebar is not a manifest. Resolume has none; moonrepo has one that
renders only the expanded category, because Docusaurus collapses the rest
client-side. Deriving the page set from the sidebar would index none of the
first site and 9% of the second.

**Coverage** — which pages exist:

| source | notes |
|---|---|
| `sitemap.xml` | most complete when present; needs scoping, since `/blog` is 385 of moonrepo's 600 URLs |
| index-page links | the seed's own declared link set, when there is no sitemap |
| `llms.txt` | **not authoritative** — 169 of moonrepo's 210, omitting every `/docs/commands/*` page |
| link-following | last resort only, under an explicit depth and page budget |

**Hierarchy** — how they nest:

| source | tier |
|---|---|
| sidebar DOM, when it covers the page set | declared, recovered by parsing |
| index-page heading grouping | declared, recovered by parsing |
| `llms.txt` order and titles | order only — moonrepo's is one flat `## Table of Contents` |
| URL path depth | inferred, nothing to check it against |

Only the first two validate. The rest warn the way `font_heuristic` warns, and
the addressability gate is what catches them when the guess was wrong.

### The seed may itself be the manifest

`resolume.com/support/avenue-arena` is a hub page whose `h1`/`h3` headings and
links declare 80 pages across 10 sections. That is author-declared structure
recovered by parsing — a first-class source, not a fallback.

Two parsing details decide whether it works. The heading sits *inside* the
anchor (`<a><h3>Title</h3>…</a>`), so assigning each link "the most recently
seen heading" is off by one and mislabels every page — visible only by
checking a slug against its title. And every link appears twice from duplicate
desktop and mobile rendering, so the link set needs dedup before it is a
frontier.

## The frontier

The frontier is the coverage set, scoped. The path prefix is a **containment
check applied to discovered links, not the source of them**.

Deriving the frontier from the seed's path prefix fails outright on a hub
page: the Resolume seed is `/support/avenue-arena` while every document is at
`/support/en/*`, so prefix-derivation yields nothing. Scoping still matters —
without it moonrepo's crawl pulls 385 blog posts — but it filters a declared
set rather than generating one.

Breadth-first link-following exists only when neither a sitemap nor an index
page yields anything, and then under an explicit depth and page budget.

## Completeness is a gate

Three sets exist by the end of a crawl: pages coverage declared, pages the
hierarchy source places, and pages actually fetched. Their disagreements mean
different things.

**Declared, never fetched.** A broken link on a large site is ordinary and
must not refuse the ingest; a large share of them means the site was not
navigable and the index is a silent partial. Every unreachable page is
reported, and past a share threshold the ingest fails. An index covering 60%
of a site while reporting success is the web's version of the unaddressable
failure — legally formed, internally consistent, and quietly wrong.

**Fetched, but absent from the hierarchy source.** Placed by URL path depth
and reported — *not* excluded. Excluding them discards 41 of moonrepo's 210
pages, including every command reference, which is precisely the silent
partial the gate above exists to prevent.

**In the hierarchy source, absent from coverage.** Sitemaps and hub pages are
each routinely incomplete, so the union is the frontier and this is not a
finding.

## Acquisition

**Politeness.** `robots.txt` honored where present, with an explicit override
for sites the operator runs. Two concurrent requests by default and a request
interval; `Retry-After` and `429` respected with backoff; a `User-Agent` that
identifies the tool; a page budget that caps a misconfigured seed.

**URL normalization.** `moonrepo.dev/docs/` is a 404 and `moonrepo.dev/docs`
is the page. Trailing slashes, default documents and case all need settling
before a URL is a frontier key, or the same page is fetched twice and a real
page is recorded as missing. Where a sitemap exists it is the authority on the
canonical form.

**Soft 404s.** moonrepo's 404 response carries 17 KB of HTML and 614
characters of extractable text. Its status code is honest, but a site that
answers `200` on its not-found page would feed that text to the index and the
completeness gate would count it as a success. Status is checked first, and a
page whose extracted text matches the site's known not-found body is treated
as unfetched.

**Redirects.** Bounded hop count, and every hop re-validated by the guard —
validating only the seed leaves it trivially bypassable. A hop that leaves the
host or the scope is dropped, reported.

**Canonical URLs.** `<link rel="canonical">` collapses the duplicates that
versioned documentation produces when a seed spans `latest/` and `v2.1/`.

**Client-rendered pages.** Detected and named rather than ingested as empty
shells: a page whose body yields under a floor of extractable text while
carrying substantial script mass is reported by `inspect` with that reason.
Headless rendering is a follow-up behind a flag, not a dependency in the
critical path — both probed targets pre-render.

## The fetch cache is a separate database

`var/fetch-cache.db`, not the search database. Different lifecycle and
different reader: the search database is read by the Go server on every
request and is the artifact shipped for offline use; the cache is worker-only,
disposable, and holds raw HTML that would otherwise bloat it permanently.

Keyed on normalized URL, holding final URL, status, content type, `ETag`,
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

Ordered by blast radius ascending.

### Landed

| # | scope |
|---|---|
| 1 | Read `<pre>` verbatim so highlighted code keeps its lines; emit it as its own block when nested |
| 2 | `documents.source_kind`, `chunks.url`, `chunks.fragment`; schema 4→5 both sides |
| — | Generate the Go query layer from `python/docsearch/schema.sql` with sqlc |
| — | Read the store through the generated queries |
| 3 | Library roots become a list, named in `add_document`'s description |
| 4 | `urlguard`: scheme, host, resolution, redirect re-validation, uniform error — `internal/urlguard/`, `urlguard.py`, and `testdata/urlguard-addresses.txt` as the shared verdict table |
| 5 | Fetch cache database and the fetcher: normalization, conditional GET, rate limit, robots, redirects — `fetch.py`, `fetchcache.py` |
| 6 | Coverage: `sitemap.xml` including index files, `llms.txt`, `robots.txt` directive, index-page link sets, soft-404 detection — `discover.py` |
| 7 | Hierarchy: sidebar DOM, hub-page heading grouping, URL path depth, and the placement rule for pages no source mentions — `nav.py` |

None of 4–7 is reachable from the CLI or the worker yet: they are libraries with
tests, and PR 9 is what wires them in. The v5 columns `documents.source_kind`,
`chunks.url` and `chunks.fragment` are likewise in place and unwritten.

### Remaining

| # | scope | touches | after |
|---|---|---|---|
| 8 | Crawl orchestration: frontier, scope, budget, resume, progress, cancellation | `crawl.py` (new) | 6, 7 |
| 9 | The `Source` abstraction: `ingest_source` replaces `ingest_file`; sections from nav position, three-way cross-validation, completeness gate | `site.py` (new), `ingest.py`, `cli.py`, `worker.py`, `structure.py` | 2, 8 |
| 10 | Cross-page chrome removal | `site.py` | 9 |
| 11 | `inspect` site mode: live dry run against a URL | `inspect.py` | 8 |
| 12 | Threshold recalibration against a doc-site corpus | `structure.py`, `verify.py` | 9 |
| 13 | `refresh`: conditional re-crawl of an ingested site | `crawl.py`, `cli.py` | 9 |

PR 9 is the only change touching `ingest.py`, `cli.py` and `worker.py`
together, so everything that would collide with it is sequenced behind it.

New dependency: `httpx`. Parsing reuses the `lxml` and `beautifulsoup4`
already present.

## Verification

Tests are black-box and never touch the network. Fixture sites are served from
a local HTTP server on an ephemeral port, which gives real semantics —
redirects, `304`, `Retry-After`, malformed sitemaps, broken links, trailing
-slash 404s, soft 404s — without an external dependency. Fixtures cover both
probed shapes: a generator with a sitemap and a collapsed sidebar, and a hub
page with neither.

End to end, before any of this is called done:

1. `docsearch inspect <url>` reports the discovered page count, the coverage
   and hierarchy sources it would use, their disagreement, and any
   client-rendered pages, without writing anything.
2. `docsearch add <url>` with no other flags crawls, extracts, chunks and
   indexes, and reports what it found.
3. A second `docsearch add` of the same URL transfers almost nothing and
   reports `unchanged`.
4. `docsearch verify` grades the site, with PR 12's constants in place.
5. `docsearch-eval --self-label` shows retrieval functioning on the index.
6. `search` with a `section_filter` of a nav subtree returns only that
   subtree, and results carry a URL that resolves.
7. The database answers with the network disconnected.

## Deferred

**Headless rendering** for client-rendered sites, behind an explicit flag.
`inspect` names them in the meantime, so the gap is visible rather than
silent.

**Authenticated sites.** Targets are public. Adding them later means
credential storage, redaction in logs and errors, and a rule that credentials
never survive a cross-host redirect.

**Incremental re-chunk.** `refresh` refetches only what changed, then
re-chunks the whole site. Correct, and wasteful on a large site whose single
page moved.

**Content upload.** Bytes over the wire rather than a path, which is what a
client that does not share the server's filesystem would need. Out of scope
while the server and its callers are on one machine. It would need a staging
area with a quota, size caps, content sniffing rather than a declared type,
decompression limits for the zip-container formats, and digest-keyed
replacement — an upload has no `source_path` to key on. The `Source`
abstraction is where it would attach.

**`llms-full.txt`.** moonrepo publishes 1.3 MB of its whole corpus as text. A
single fetch is tempting, but it carries no per-page URLs, so nothing in it
can be cited. Fallback at best.

## Open

**Corpus for PR 12.** `ADDRESSABLE_MIN = 0.50`,
`ADDRESSABILITY_MIN_CHUNKS = 25`, `HEADLESS_DEGRADED_RATE = 0.02` and the
`verify` grading rates were measured on four manual corpora. Doc sites are
shorter-paged, shallower and code-heavy. Two targets are probed and recorded —
`resolume.com/support` (80 pages, no generator, hub-page index) and
`moonrepo.dev/docs` (210 pages, Docusaurus, sitemap and `llms.txt`). A Sphinx
target and an mdBook target are still wanted, and at least one should be
API-reference-heavy, since near-identical method stubs are the shape
`classify_kinds` already detects.

**Unreachable-page threshold.** The share of declared pages that may fail to
fetch before the ingest is refused rather than reported. Set it from the
corpus rather than picking a number now.
