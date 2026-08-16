# Doc-site corpus — the thresholds hold, the chunker did not

Date: 2026-08-15 · Corpus: five documentation sites, 474 pages
Harness: `scripts/measure_site_corpus.py`

The plan defers recalibrating the grading constants until there is a doc-site
corpus to measure, on the grounds that doc sites are shorter-paged, shallower
and code-heavy than the four manuals the constants came from. This is that
measurement.

## Verdict: do not move the constants

Four of the five sites land at **0.78–0.82** distinct heading paths per chunk,
inside the band the manual corpora set (0.93, 0.84, 0.81 where retrieval works;
0.29 where structure was never derived). `ADDRESSABLE_MIN = 0.50` separates
them exactly as it was built to. One set of constants serves both document
kinds, which is the outcome worth having and not the one expected.

What the measurement found instead was two defects in the chunker, both of
which made doc sites *look* like a calibration problem.

## The corpus

| site | generator | pages | hierarchy source |
|---|---|---|---|
| `docs.pytest.org` | Sphinx, API-reference-heavy | 57 | index_page |
| `www.sphinx-doc.org` | Sphinx | 49 | index_page |
| `moonrepo.dev/docs` | Docusaurus | 215 | url_path |
| `rust-lang.github.io/mdBook` | mdBook | 33 | url_path |
| `resolume.com/support` | hand-built hub page | 120 | index_page |

Two of the four generators render their navigation **client-side**. Docusaurus
collapses categories, as the earlier probe recorded; mdBook now builds its
table of contents into `<mdbook-sidebar-scrollbox>` and ships zero sidebar
links in the HTML. In both cases the coverage check rejected the fragment it
found and fell back to URL path, which is the designed behaviour working. **The
`url_path` fallback is the common case for generated sites, not the exception.**

## What the numbers said, in three passes

| | pytest | sphinx | moonrepo | mdBook | resolume |
|---|---|---|---|---|---|
| addressability, as first measured | 0.15 | 0.32 | 0.71 | 0.46 | 0.51 |
| after the grouping fix | 0.90 | 0.88 | 1.00 | 0.98 | 0.98 |
| after the merge fix | **0.82** | **0.80** | **0.78** | **0.81** | 0.40 |
| median tokens, after both | 263 | 289 | 123 | 159 | 33 |

**Pass 1 was measuring a bug.** `_group` keyed units on the section alone, and
a unit keeps only its first block's heading path — so every block after the
first in a section had its path discarded. Invisible for the paginated formats,
where a path is built from the section's own ancestry and cannot vary within
one. A site breaks that assumption by design: the section is the page's
position in the navigation and is constant across the whole page. pytest's
getting-started page has nine internal headings and contributed three chunks
all reading `Get Started`. Recalibrating against pass 1 would have tuned the
thresholds to fit that.

**Pass 2 over-corrected.** With paths preserved, `_merge_small` still refused to
merge anything carrying a section, so every heading became its own chunk:
Resolume's 120 pages became 4,272 chunks at a median of 16 tokens. Shredded
past answering anything — and silent, because `grade()` excludes numbered
chunks from its fragmentation check and every chunk of a site is numbered. What
a document declares is the boundary *between* two units, not the presence of a
section on one; the test is now whether the next unit sits in a different
section, which reads identically for a paginated format where a section holds
one unit.

**Pass 3 is the honest measurement**, and it is where the constants are
vindicated.

## Resolume, the outlier, is correct

0.40 addressability, a 33-token median, 1,575 of 2,502 chunks under 50 tokens —
and `unaddressable` fires. That is the right answer: the busiest single page is
`/footage/styles` with 679 chunks, which is the **product catalogue**, not
documentation. Each entry is a short marketing blurb.

It is in the index because the seed's own links are in scope *by declaration*
rather than by path prefix — the rule that exists because Resolume's seed sits
at `/support/avenue-arena` while its documents are at `/support/en/*`, so
prefix-filtering discards the entire site. The same rule admits the store.

So the grading is right and the **scoping** is the open question. The probe
recorded 80 pages for this target; the crawl finds 120. Options, none taken
here: exclude declared links whose path prefix differs from the majority of the
declared set; or let `add` take a scope hint. Both want more than one
hand-built site to decide against.

## Also found, not fixed

**`refresh --from-cache` cannot re-chunk after a code change.** The content
digest is taken over fetched bytes, so an unchanged site short-circuits to
`unchanged` before extraction runs. Re-indexing a whole site after a chunker
change with no requests is a capability the plan claims and the cache was built
for, and it does not work — four of five sites reported `unchanged` after both
fixes above, and the corpus had to be re-chunked by deleting the documents
first. The digest needs to cover the code version that produced the chunks, not
only the bytes that went in.

**`grade()` is blind to a shredded site.** `mergeable` excludes any chunk with
a section, and every chunk of a site has one, so the fragmentation check never
runs on a site at all. Pass 2's 16-token median produced no finding. The check
wants the same treatment `_merge_small` just got: ask whether the neighbouring
chunk shares the section, rather than whether this one has one.

## Reproducing

```bash
docsearch add https://docs.pytest.org/en/stable/ --db var/corpus.db
uv run python scripts/measure_site_corpus.py --db var/corpus.db
```

Every figure above comes from that harness against the five seeds listed. The
crawler obeys `robots.txt`, spaces requests, and identifies itself.
