# Doc-site retrieval: the crawl and extraction fixes left search results unchanged

Date: 2026-10-25 · Query set: `tests/retrieval/sites.json`, 73 queries over five sites
Harness: `cmd/docsearch-eval`, which drives the same `pgstore.Search` path the MCP server uses

PRs #112 and #117 were measured on pages kept and prose extracted. Scored on
search results, neither moved anything: the builds before #112, before #117
and current `main` rank all 73 queries identically, except one informed query
that rose from rank 3 to rank 2. The set does register an extraction change:
the build before #113 differs from `main` on 11 queries. So the flat result
for #112 and #117 comes from those PRs, not from a set too coarse to notice.

## Baseline on `main` (32f96a9)

The consumer reads k=8 full chunks, so `@8` is the headline, as in
[retrieval-quality.md](retrieval-quality.md).

| site | generator | pages | chunks | n | @1 | @3 | **@8** | @20 |
|---|---|---|---|---|---|---|---|---|
| `docs.pytest.org/en/stable` | Sphinx | 57 | 991 | 15 | 67% | 93% | **100%** | 100% |
| `www.sphinx-doc.org/en/master` | Sphinx | 49 | 358 | 15 | 47% | 67% | **73%** | 80% |
| `moonrepo.dev/docs` | Docusaurus | 218 | 1,343 | 14 | 43% | 57% | **86%** | 86% |
| `rust-lang.github.io/mdBook` | mdBook | 33 | 259 | 15 | 67% | 80% | **93%** | 100% |
| `mintlify.com/docs` | Mintlify | 295 | 2,731 | 14 | 50% | 71% | **93%** | 93% |
| all | | | | 73 | 55% | 74% | **89%** | 92% |
| blind only | | | | 57 | 51% | 72% | **88%** | 91% |

| category | n | @1 | @3 | **@8** | @20 |
|---|---|---|---|---|---|
| heading-term | 22 | 64% | 77% | **91%** | 95% |
| body-term | 15 | 73% | 100% | **100%** | 100% |
| spans-boundary | 7 | 57% | 86% | **100%** | 100% |
| conceptual-slang | 23 | 22% | 43% | **74%** | 78% |
| figure-dominated | 6 | 100% | 100% | **100%** | 100% |

Hits at `@8`, by site and category:

| site | heading-term | body-term | spans-boundary | conceptual-slang | figure-dominated |
|---|---|---|---|---|---|
| pytest | 4/4 | 3/3 | 2/2 | 5/5 | 1/1 |
| sphinx | 2/4 | 4/4 | 1/1 | 3/5 | 1/1 |
| moon | 5/5 | 1/1 | 2/2 | 3/5 | 1/1 |
| mdbook | 4/4 | 4/4 | 1/1 | 3/4 | 2/2 |
| mintlify | 5/5 | 3/3 | 1/1 | 3/4 | 1/1 |

Cells of one or two queries are not evidence on their own. Read the grid for
where the set is thin, which matters for the next round of authoring more than
for any conclusion drawn from it.

## The fixes, scored

Each build crawled the five sites into its own database on the same day. One
`docsearch-eval` binary from `main` scored all four, so only the index varies.

| build | commit | what it lacks | @1 | @3 | @8 | @20 | queries that moved vs `main` |
|---|---|---|---|---|---|---|---|
| `main` | 32f96a9 | | 40 | 54 | 65 | 67 | |
| before #117 | fa2d4d1 | #117, #114, #115, #116 | 40 | 54 | 65 | 67 | 1 (p15, rank 3 to 2) |
| before #112 | c4a15fb | #112 and all of the above | 40 | 54 | 65 | 67 | 1 (p15, rank 3 to 2) |
| before #113 | 2c03091 | #113 and all of the above | 42 | 57 | 64 | 66 | 11 |

Counts are hits out of 73.

**#112 changed nothing on these sites.** The builds before and after it
produced identical chunk counts for all five sites and identical ranks for
all 73 queries.

**#117 and the PRs after it added text without moving a target.** Together
they added 204 chunks to Mintlify (2,527 to 2,731) and 31 to moonrepo, and
the only rank change in the set is p15 on pytest. pytest kept 991 chunks but
their text changed, so p15 moved either on new pytest text or on corpus-wide
IDF (below), and this run cannot tell which. The builds before #114 also
carried zero-width spaces inside heading paths
(`Shared ​ > versionFromPrototools`), which #114 removed without changing a
rank.

**#113 is the positive control.** It turned Mintlify's `span[data-as=p]`
paragraphs into blocks, taking Mintlify from 1,693 chunks to 2,527. The
effect on search is mixed. x05 ("old urls should keep working after I move
pages") went from a miss to rank 6, and x13 and x14 rose to rank 1. Three
Mintlify queries that ranked first without #113 now rank 2 or 4, and x01
fell from 3 to 5. Mintlify's `@8` rose from 86% to 93% while its `@1` fell
from 57% to 50%. One plausible reading is that pages whose prose had been
dropped now compete for the top slot. This run does not test it.

**Sites are not independent.** BM25's IDF is computed over the whole table,
so #113 also moved p04, p11, m03 and b02 on sites it never touched. The
builds on either side of #113 hold byte-identical chunks for those four
sites, so Mintlify's new text is the only thing that moved them. Changing
how one site is extracted can shift ranks elsewhere, so read a per-site delta
with that coupling in mind.

## Why the misses miss

Six queries miss at `@20`.

| query | site | cause |
|---|---|---|
| s03 google style docstrings | sphinx | `napoleon` page not crawled |
| s06 fail the build on warnings | sphinx | `sphinx-build` man page not crawled |
| s11 write a custom directive | sphinx | "Hello world" extension tutorial not crawled |
| m08 pin the node version | moon | the page never says "pin"; "node" and "version" appear on dozens of pages |
| x02 preview on my laptop | mintlify | only "preview" reaches the page, and preview deployments share it |
| m12 what breaks in moon 2 | moon | the page never says "breaks" or "upgrade"; "moon" is on every page |

**Half are crawl coverage, not ranking.** The Sphinx crawl took 49 pages, the
ones its index page's navigation declares. All three missing pages return 200
and are linked from crawled pages (`usage/extensions/index.html` links
`napoleon.html` three times), but the crawl does not follow them. Those three
misses are most of why Sphinx has the lowest `@8` of the five, at 73%.

The other three are conceptual-slang queries with no lexical bridge to the
page, the same floor `retrieval-quality.md` describes for a pure-lexical
index. Conceptual-slang stays the weakest category, at 74% `@8`.

## GitBook was not indexed

`gitbook.com/docs` failed the completeness gate on every build, so its ten
blind queries are held out of the set until it indexes:

| build | known pages | unreachable | cause |
|---|---|---|---|
| before #117 and earlier | 119 | 43 (36%) | `llms.txt` lists `.md` URLs |
| `main` | 1,285 | 786 (61%) | 500-page budget; the overflow counts as unreachable |

On `main`, #115 reads the `.md` URLs as their HTML pages, which found the
whole site, API reference included. The crawl budget stops at 500 pages,
which leaves 785 of the 1,285 never requested, and the gate counts those as
unreachable. A site larger than the budget can therefore never pass. Its
queries:

| id | category | expect | query |
|---|---|---|---|
| g01 | heading-term | GitHub & GitLab Sync | sync my space with a github repository |
| g02 | conceptual-slang | Change requests | propose edits and get them reviewed before they go live |
| g03 | heading-term | Custom domain | use my own domain for a published site |
| g04 | body-term | Variables | variables to reuse the product version number across pages |
| g05 | conceptual-slang | authenticat | password protect my published docs so only customers can read them |
| g06 | heading-term | Adaptive content | show different content to different users |
| g07 | body-term | Migrate to GitBook | import content from Confluence or Notion |
| g08 | figure-dominated | Document an API | render an api endpoint from an openapi spec in a page |
| g09 | conceptual-slang | Site analytics | see which pages readers look at most |
| g10 | heading-term | Reusable content | reuse a block of content across several spaces |

## How the set was written

57 of the 73 queries are blind. They were written from knowledge of each
project's documentation, in an agent's phrasing, and hashed before the first
crawl started. The other 16 carry `blind: false`: I wrote them after reading
the index's heading paths, and the harness reports them separately.

The `_method` block in `sites.json` records two kinds of post-crawl change to
blind `expect` values, and each changed query says which in its `note`. Seven
were corrected to the intended page's live title, read from the site's
`llms.txt` or the page's `<h1>`. Nine were narrowed because the blind value
matched many pages ("Docker" matched ten moonrepo pages). Narrowing can only
turn a hit into a miss. No query text changed. One blind query was dropped,
because the moonrepo page that answered it no longer exists.

`expect` is a case-insensitive substring of `heading_path`. moonrepo, mdBook
and Mintlify take their hierarchy from URL paths, so their heading paths start
with path segments (`Guides > Docker > Docker integration`). Some expects
name that path, which ties them to the hierarchy the crawler derives. A
change to how URL-path hierarchy is rendered will move those queries, and
that move would be real, since an agent filters by the same section names.

mdBook's `print.html` holds the whole book on one page, so most mdBook
answers are indexed twice and an expect matches either copy.

## Reproducing

The sites change, so a re-crawl can move a baseline with no code change.
Compare builds against crawls taken on the same day, never against the
numbers above.

```bash
# A scratch cluster on its own port, provisioned the way the compose file does.
docker run -d --name siteeval-pg -e POSTGRES_PASSWORD=siteeval \
  -p 127.0.0.1:55440:5432 \
  -v "$PWD/deploy/docker/initdb:/docker-entrypoint-initdb.d:ro" \
  timescale/timescaledb-ha:pg17

export DOCSEARCH_TOKEN=$(openssl rand -base64 48)
DOCSEARCH_DSN=postgres://docsearch_owner:docsearch@127.0.0.1:55440/docsearch?sslmode=disable \
  go run ./cmd/docsearch migrate
DOCSEARCH_DSN=postgres://docsearch_app:docsearch@127.0.0.1:55440/docsearch?sslmode=disable \
  go run ./cmd/docsearch-server --root "$(mktemp -d)" --addr 127.0.0.1:18801 \
  --instance-label siteeval &

export DOCSEARCH_SERVER=http://127.0.0.1:18801
for seed in https://docs.pytest.org/en/stable/ https://www.sphinx-doc.org/en/master/ \
  https://moonrepo.dev/docs https://rust-lang.github.io/mdBook/ https://mintlify.com/docs; do
  go run ./cmd/docsearch add "$seed"
done

go run ./cmd/docsearch-eval -queries tests/retrieval/sites.json \
  -dsn postgres://docsearch_app:docsearch@127.0.0.1:55440/docsearch?sslmode=disable
```

To score another build, check it out, crawl into a second database on the
same cluster, and run the same `docsearch-eval` binary against both. The
doc_ids in `sites.json` are the ones the crawler derives from these seeds;
the harness refuses a query set whose documents declare none.

The harness's cross-document and agentic-loop sections do not apply to this
set. The agentic loop runs only on section-numbered documents, and the
cross-document section runs three fixed queries chosen for an earlier corpus.
