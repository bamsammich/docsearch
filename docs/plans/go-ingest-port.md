# Porting ingest to Go

Phase 03 of v2. The Python ingest pipeline moves to Go, one package at a time,
and each package ships only when its output matches Python's on every document
in the library. Python stays in the tree as the reference until the last
package lands, then leaves.

Storage does not change in this phase. The Go worker writes the SQLite schema
the Go server already reads, so `docsearch-eval` and the running MCP server
keep working throughout, and every step can be checked end to end. Postgres
arrives in phase 04, behind the store interface this phase introduces.

The crawler's response cache goes the same way, in the same phase. It is an
interface, `fetch.Cache`, with a SQLite implementation, which is what the
Python worker writes today; phase 04 adds a Postgres one and the fetcher does
not change. Both have to move together: a worker in a container loses a local
file when it restarts, a restarted crawl that cannot resume is most of what
the cache was for, and two workers cannot share a file at all.

After phase 04 the deployment holds no SQLite file. The driver stays in the
tree only while v1 is still readable.

## Layers

The port follows a clean-architecture layering. Rules sit in a domain that does
no I/O, orchestration sits in services that depend only on interfaces they
declare, and everything that touches a file, a network or a database
implements one of those interfaces.

| layer | packages | depends on | tested with |
|---|---|---|---|
| domain | `internal/domain`, `internal/pystr` | nothing | testify suites |
| application | `internal/service` | domain, and the ports it declares | testify suites, with mockery mocks of those ports |
| infrastructure | `internal/adapter/...`, `internal/site`, `internal/repository/...` | domain and service ports | testify suites on small fixtures |
| API | `internal/api/mcpapi`, `internal/api/connectapi` | service | testify suites over mocked services |
| integration | `test/integration` | everything | ginkgo and gomega BDD suites |

Unit tests are testify suites: one `suite.Suite` per test
file, run from a single `Test...` function, with table cases as `s.Run`
subtests and assertions through the suite (`s.Equal`, `s.Require()`). Suites
run their methods serially on one `*testing.T`, so they do not call
`t.Parallel()`.

`.mockery.yaml` arrives with the first port, the extractor interface in step
6b, so no mock exists before an interface does. The port belongs to the ingest
service that calls it, and that service is written in 6b; until then the
adapters are plain functions behind a suffix registry.

## Two front doors: MCP for Claude, ConnectRPC for programs

The service layer gets two API adapters. Claude only
speaks MCP, so MCP cannot be replaced; every other client gets a typed
ConnectRPC API instead of hand-built HTTP.

| client | door |
|---|---|
| Claude: claude.ai, Desktop, Code | MCP, `internal/api/mcpapi` |
| `docsearch` CLI | ConnectRPC; with several users it authenticates through the server rather than opening the database |
| `docsearch-sync` beside Paperless | ConnectRPC |
| upload page and any later web UI | Connect-Web |
| file bytes on upload | plain HTTP `PUT` to a signed link, never an RPC message |

Both doors verify tokens through the same check, a Connect interceptor on one
side and MCP middleware on the other.

The MCP tools stay hand-written. `redpanda-data/protoc-gen-go-mcp` can generate
MCP tools from a proto service, but it returns each response's raw proto JSON,
and docsearch's results are rendered for a model: full chunk text, relevance on
a 0 to 1 scale, figure warnings, quality notes. Tool descriptions and server
instructions are tuned prose that proto comments would hold poorly.

### Enums line up with proto

Domain enums start at `iota + 1`, so a zero value means unset, and proto
enums reserve `0` for `UNSPECIFIED`, so the two share numbering and the
boundary conversion is a cast.

The cast is written out rather than generated. goverter was the plan, and it
earns its place where a conversion walks a struct field by field; here the
numbering is shared by construction, so there are no fields to walk. What the
cast needs instead is proof that the numbering still agrees, which
`internal/api/connectapi` states as constant expressions: a value renumbered
on either side fails to compile rather than mislabelling a document's grade in
a client.

| domain | proto |
|---|---|
| `QualityOK = iota + 1` | `QUALITY_OK = 1` |
| `QualityDegraded` | `QUALITY_DEGRADED = 2` |
| `QualityFailed` | `QUALITY_FAILED = 3` |

Persisted values keep their strings: each enum marshals as text, so
`documents.warnings` still reads `"ok"`, `"degraded"`, `"failed"` and parity
with Python is unaffected.

The text is a literal table beside the constants rather than a generator's
output. Both `dmarkham/enumer` and `abice/go-enum` would write these methods,
and each derives the text from how a constant is spelled; the text here is the
contract with a v1 database and with Python, and one value is
`none (blank-line paragraphs)`. A transform rule between a constant and the
byte that reaches `documents.warnings` buys nothing and can be got wrong, so
11 values are written out.

`StructureSource` is a closed set, since format adapters and the site
navigation are its only producers. Closing it added `url_path`, a source
`nav.py` produces that the Go constants had never listed, and deleted the
second copy of `sidebar_dom`, `index_page` and `url_path` that
`internal/site/nav` was carrying.

## How parity is checked

Two kinds of test, because the library cannot be committed:

| test | input | runs where |
|---|---|---|
| unit tests ported from `tests/` | small synthetic fixtures already in the Python tests | everywhere |
| golden tests | synthetic documents in `testdata/adapters/`, with the Python adapters' extraction of each | everywhere |
| parity tests | Python reference output in `var/parity/` | only where that output exists; skipped otherwise |

The library holds copyrighted manuals, so their extracted text never goes into
the repository, and neither does the tool that produces the reference output:
a local script, ignored by git, runs the Python pipeline over every document
in a library on the maintainer's machine and writes each stage's output as
JSON under `var/parity/`. `DOCSEARCH_PARITY_DIR` points the suite at another
directory. Each Go package's parity test reads the stage
before it as input and compares its own output against the stage after, so a
package is tested on Python's exact inputs, not on another Go package's
approximation of them.

A parity test passes on exact equality. A difference is either a Go bug, or a
Python behaviour the port deliberately drops, which the package then documents
with its reason. A drop that would change output on the library also needs the
measurement that justified it. The port drops one behaviour so far: python-docx
refuses a package holding two relationships of one type, and the Go adapter
reads the first, since refusing a readable document only loses its text.

### PDF is checked in two halves

PDFium and MuPDF do not report the same lines, so the PDF adapter cannot
match Python end to end. It splits where the spike split it.

`pdf.Build`, everything after the engine, is pure and is held to Python
exactly: the parity script dumps the primitives PyMuPDF gave for each PDF,
and Build must turn them into the same extraction, diagnostics included.

The engine is held to the chunk structure instead, by the two measures the
spike used: the overlap of the heading paths, and of the chunk starts. Each
document's scores are recorded beside its reference output, and a run fails
if either falls below what was recorded. Every manual records 1.0; the
two-column journal papers record the gaps
[the spike](../research/pdfium-spike.md) explains.

### The site pipeline is checked against a site this project invents

A crawl has no input file to hand both implementations, and pointing the
parity check at a real documentation site would make it depend on a
stranger's uptime and on whatever they published this week. So each fixture
under `testdata/site` is a site written here: a route manifest, the pages it
answers with, and the Python pipeline's crawl of it, all committed.
`scripts/site_goldens.py` serves the manifest over loopback and writes the
golden; the Go spec serves the same manifest from the same files and must
produce the same extraction and the same chunks.

Both servers read the manifest rather than serving a directory, because a
directory listing, a guessed content type or the body of a 404 differs
between two static file servers, and any of those would show up as a
difference between the two pipelines.

Two sites, for the two shapes a crawl takes. `declared` publishes a sitemap,
so nothing is walked: it carries a nested sidebar, a page under two spellings
with a canonical link, a declared page that 404s, and a colophon on every
page for the chrome check. `walked` publishes no manifest and answers 200 for
addresses that do not exist, so link-following finds the pages and the
not-found template is what marks a dead link unreachable.

## Order

Each row is one pull request into `v2`. A row starts when the rows it reads
from have landed.

| step | Python | Go package | parity check |
|---|---|---|---|
| 1 | `tokens.py`, `blocks.py`, `chunker.py` | `internal/domain`, `internal/pystr` | Python's extraction in, identical chunks out |
| 2 | `structure.py` | `internal/domain` | identical quality grade and findings |
| 3 | `adapters/text.py`, `markdown.py`, `docx.py`, `html.py` | `internal/adapter` and a subpackage per format | identical extraction |
| 4 | `adapters/pdf.py` | `internal/adapter/pdf`, on go-pdfium | Build reproduces Python from PyMuPDF's primitives; the engine holds its recorded chunk structure |
| 5a | `fetch.py`, `fetchcache.py` | `internal/site/fetch` | behaviour ported from `tests/test_fetch.py`; a fetcher talks to the network, so it has no output to compare |
| 5b | `discover.py`, `crawl.py` | `internal/site/discover`, `internal/site/crawl` | behaviour ported from the Python tests; a crawl needs a site to compare over, which 5d supplies |
| 5c | `nav.py`, `site.py` | `internal/site/nav`, `internal/site` | as 5b |
| 5d | — | — | synthetic sites under `testdata/site`, served to both pipelines: identical extraction and identical chunks |
| 6a | — | `internal/domain` | typed `Quality`, `ChunkKind` and `StructureSource`; every extraction still persists the text it did |
| 6b | `ingest.py` | `internal/service/ingest` and the ports it declares, `internal/source/file`, `internal/source/site` | testify suites over mockery mocks of those ports |
| 6c | `db.py` | `internal/repository/sqlite` | every write run against a real index built from `python/docsearch/schema.sql` |
| 6d | `worker.py` | `internal/service/worker`, its queue in `internal/repository/sqlite`, `cmd/docsearch-worker`, and a progress hook on `internal/site/crawl` | the claim's races and the lease against a real queue; the loop over mocked ports |
| 6e | — | `proto/docsearch/ingest/v1`, `internal/api/connectapi`, `internal/source` | testify suites driving the real Connect stack over `httptest`, against a mocked service |
| 6f | — | `test/integration` | an index built by Go passes `docsearch verify` and matches the eval, in a ginkgo full-stack suite; a structure mismatch refuses the document, writes nothing, and fails the job permanently |
| 7 | `cli.py`, `inspect.py`, `verify.py` | `cmd/docsearch`, a ConnectRPC client of the server | same commands, same reports |

`urlguard.py` already has a Go twin in `internal/urlguard`, held to the same
table of addresses; step 5 deletes the Python copy.

## Improvements parity holds back until step 6

Parity guards the port against accidental change; it does not claim Python's
output is the best available. Some text the Python adapters never read stays
unread in Go too, because reading it would break parity. Once step 6 retires
the Python pipeline, each of these lands as its own pull request, judged by
the retrieval eval before and after rather than by byte equality.

| format | Python misses | Go-native change |
|---|---|---|
| DOCX | table cells, tracked insertions, content controls | walk the whole body, not only its top-level paragraphs |
| Markdown | setext headings, indented code blocks, headings inside lists | parse with `goldmark`, a CommonMark parser, instead of line regexes |
| DOCX | the "Word Document" fallback title, and "Heading 0" replacing the innermost heading | drop both quirks |

## Python behaviour Go does not share

Each of these changes output silently if ported naively.

| Python | Go equivalent |
|---|---|
| `\w` matches Unicode letters and digits | `[\p{L}\p{N}_]`; Go's `\w` is ASCII |
| `[^\W\d_]` is a letter, including numeric letters like Ⅻ | `[\p{L}\p{Nl}\p{No}]` |
| `str.splitlines()` splits on ten separators, `\x1c` and ` ` among them, with `\r\n` as one | a helper matching the same set |
| `str.strip()` treats `\x1c`–`\x1f` as whitespace | a helper; `strings.TrimSpace` does not |
| `round()` rounds halves to even | `math.RoundToEven` |
| `len(text)` counts code points | `utf8.RuneCountInString` |
| `int(x)` truncates toward zero | `int(x)` on a float, same |
| `Path.read_text(errors="replace")` gives a truncated sequence one U+FFFD, and reads `\r\n` and `\r` as `\n` | `pystr.ReadText`; ranging over bytes in Go gives one U+FFFD per byte |
| `PurePath.stem` keeps `.bashrc` whole | `pystr.Stem`; `filepath.Ext` takes all of `.bashrc` as the extension |
| PyMuPDF writes page text with "\n" line endings, the last line included | the PDF engine rewrites PDFium's "\r\n" text the same way; nothing parses that text, and a search result shows it |
| `urllib.robotparser` takes the first rule that matches a path | `grobotstxt`, Google's matcher, takes the longest; the two agree except where a robots.txt both allows and disallows one path |
| lxml parses HTML and drops content after `</body>` | `x/net/html` follows HTML5 and moves it into the body; accepted, since a browser reads such a page the HTML5 way |
