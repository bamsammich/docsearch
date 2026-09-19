# Porting ingest to Go

Phase 03 of v2. The Python ingest pipeline moves to Go, one package at a time,
and each package ships only when its output matches Python's on every document
in the library. Python stays in the tree as the reference until the last
package lands, then leaves.

Storage does not change in this phase. The Go worker writes the SQLite schema
the Go server already reads, so `docsearch-eval` and the running MCP server
keep working throughout, and every step can be checked end to end. Postgres
arrives in phase 04, behind the store interface this phase introduces.

## Layers

The port follows the layering RightNow uses. Rules sit in a domain that does
no I/O, orchestration sits in services that depend only on interfaces they
declare, and everything that touches a file, a network or a database
implements one of those interfaces.

| layer | packages | depends on | tested with |
|---|---|---|---|
| domain | `internal/domain`, `internal/pystr` | nothing | testify suites |
| application | `internal/service` | domain, and the ports it declares | testify suites, with mockery mocks of those ports |
| infrastructure | `internal/adapter/...`, `internal/site`, `internal/repository/...` | domain and service ports | testify suites on small fixtures |
| API | `internal/api/mcpapi`, `internal/api/connectapi` | service | testify suites over mocked services, as RightNow's API tests are |
| integration | `test/integration` | everything | ginkgo and gomega BDD suites |

Unit tests are testify suites, as in RightNow: one `suite.Suite` per test
file, run from a single `Test...` function, with table cases as `s.Run`
subtests and assertions through the suite (`s.Equal`, `s.Require()`). Suites
run their methods serially on one `*testing.T`, so they do not call
`t.Parallel()`.

`.mockery.yaml` arrives with the first port, the extractor interface in step
3, so no mock exists before an interface does.

## Two front doors: MCP for Claude, ConnectRPC for programs

The service layer gets two API adapters, the pair RightNow runs. Claude only
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

Domain enums start at `iota + 1`, as the go-project skill requires, and proto
enums reserve `0` for `UNSPECIFIED`, so the two share numbering and the
boundary conversion is a checked cast. RightNow generates those converters
with goverter.

| domain | proto |
|---|---|
| `QualityOK = iota + 1` | `QUALITY_OK = 1` |
| `QualityDegraded` | `QUALITY_DEGRADED = 2` |
| `QualityFailed` | `QUALITY_FAILED = 3` |

Persisted values keep their strings: each enum marshals as text, so
`documents.warnings` still reads `"ok"`, `"degraded"`, `"failed"` and parity
with Python is unaffected. `Quality`, `ChunkKind` and `StructureSource` move
from string constants to typed enums in step 6, when the proto that mirrors
them is written. `StructureSource` becomes a closed set, since adapters are its
only producers.

## How parity is checked

Two kinds of test, because the library cannot be committed:

| test | input | runs where |
|---|---|---|
| unit tests ported from `tests/` | small synthetic fixtures already in the Python tests | everywhere |
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
with the measurement that justified dropping it.

## Order

Each row is one pull request into `v2`. A row starts when the rows it reads
from have landed.

| step | Python | Go package | parity check |
|---|---|---|---|
| 1 | `tokens.py`, `blocks.py`, `chunker.py` | `internal/domain`, `internal/pystr` | Python's extraction in, identical chunks out |
| 2 | `structure.py` | `internal/domain` | identical quality grade and findings |
| 3 | `adapters/text.py`, `markdown.py`, `docx.py`, `html.py` | `internal/adapter/...`, behind a service-declared extractor port | identical extraction |
| 4 | `adapters/pdf.py` | `internal/adapter/pdf`, on go-pdfium | the spike 1 rules; identical chunks on the manuals |
| 5 | `fetch.py`, `fetchcache.py`, `discover.py`, `nav.py`, `crawl.py`, `site.py` | `internal/site/...` | identical extraction from the same fetch cache |
| 6 | `ingest.py`, `db.py`, `worker.py` | `internal/service`, `internal/repository/sqlite`, `cmd/docsearch-worker`, the service's proto and `internal/api/connectapi`, typed domain enums | an index built by Go passes `docsearch verify` and matches the eval, in a ginkgo full-stack suite; a structure mismatch refuses the document, writes nothing, and fails the job permanently |
| 7 | `cli.py`, `inspect.py`, `verify.py` | `cmd/docsearch`, a ConnectRPC client of the server | same commands, same reports |

`urlguard.py` already has a Go twin in `internal/urlguard`, held to the same
table of addresses; step 5 deletes the Python copy.

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
