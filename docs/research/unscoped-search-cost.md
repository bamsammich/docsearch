# Unscoped search cost

Date: 2026-11-01 · `pg_textsearch` 1.4.0 on `timescale/timescaledb-ha:pg17` ·
harness `spike/searchcost`

An unscoped search used to run one candidate query per ready document, and
its cost grew with the number of documents times the size of the library.
`pg_textsearch` cannot narrow its ordered scan to one document. It orders the
whole partition and drops the rows of every other document afterwards, so each document's
query walked past every other document's matches. Search now walks the
ordering once and returns the same results.

## Timings in one library

The library is the phase 0 corpus of synthetic documents of 20 to 120 chunks
over a skewed 20,000-word vocabulary, grown inside one partition. Each figure
is the median of ten three-word queries run five times each, at k = 8, with a
second account's partition present, as any deployment with two accounts has.

| documents | before | after | before, per document | after, per document |
|---|---|---|---|---|
| 20 | 13.6 ms | 16.5 ms | 0.68 ms | 0.83 ms |
| 200 | 410 ms | 140 ms | 2.05 ms | 0.70 ms |
| 500 | 1,262 ms | 257 ms | 2.52 ms | 0.51 ms |

The 20-document library is 3 ms slower. Its walk passes every match, and
with a second partition present each one costs about 30 µs (see the parent
index name, under Found on the way). The per-document figure no longer grows with the library.

## Retrieval is unchanged

On a crawl of the five sites in `tests/retrieval/sites.json` (5,797 chunks),
every query of that set ran unscoped at k = 8, at k = 20 and with keyword
reference included. Both builds returned identical chunks, order and relevance
for all 219 searches, and took the same time, 5.4 s
against 5.5 s in all. `docsearch-eval` printed byte-identical reports from
both builds: 40, 54, 65 and 67 of 73 at @1, @3, @8 and @20.

On the synthetic corpus the harness compares every shape in the table below
with the per-document queries, as each document's pool of scores, for every
query at every size, and all of them agree. `pgstore`'s tests check that an
unscoped search ranks each document's results as a scoped search does.

## What was tried

| shape | 20 docs | 200 docs | 500 docs | why not |
|---|---|---|---|---|
| query per document (before) | 13.6 ms | 410 ms | 1,262 ms | each query walks every document's matches |
| one `LATERAL` statement over documents | 9.9 ms | 651 ms | 1,177 ms | the same walk, once per document |
| btree pre-filter, standalone score per document | 76 ms | 699 ms | 1,311 ms | scores every chunk of every document |
| one walk to the end, window per document | 8.1 ms | 71 ms | 128 ms | scores every match, not just the pools; on the crawl it was planned as a scan and a sort, at 268 ms a search |
| cursor over the walk, stopping when every pool is full | 24 ms | 203 ms | 376 ms | each fetched row is scored standalone |
| one walk of keys, then score the pools (after) | 16.5 ms | 140 ms | 257 ms | |

The window is fastest here because these documents are small, so the pools
hold nearly every match anyway. The crawl is the opposite, with five large
documents, a few hundred to 5,797 matches a query, and 160 rows in the pools
at k = 8. Scoring every match there costs more than the per-document queries
did.

`pg_textsearch` offers no filtered top-k. Its README says other conditions on
an ordered BM25 scan are post-filters, and a pre-filter means a btree scan and
standalone scoring, which the table measures. A partial BM25 index per
document would score with that document's statistics alone, changing every
score, and would need DDL at each ingest.

## What makes the walk cheap

The walk is cheap only in one shape. Each condition was measured over the
crawl's 5,797 chunks with `EXPLAIN ANALYZE` and the server's statement log,
where the walk takes 3 ms.

| condition | cost without it |
|---|---|
| carries keys only | a projected `<@>` is scored standalone, about 60 µs a row |
| sorts disabled for the one statement | under row-level security an unbounded `ORDER BY` is planned as a scan of the `user_id` index, a standalone score for every row and a sort: 260 ms |
| an unnamed statement | from the driver's cached prepared statement the same plan took 190 ms |
| the matches' zero-score test made in Go | in SQL the planner moves it into the scan and scores each row twice |

Scoring the rows each pool keeps is the work the per-document queries also
did, so what remains is a cost per document.

## Found on the way

**A second account's search finds nothing.** The query names the parent
index, `chunks_bm25`, and `pg_textsearch` scores through it with the first
partition's statistics (upstream issue 552). Every row in any other
partition scores zero, and the `score < 0` test drops them all. A 50-document
library in a second partition returned no results through `pgstore.Search`.
The `default` account's partition is the first, which is why the store's
tests and a single-account deployment never see it. The phase 0 spike's
libraries were each in their own partition, so the timings it compared
across spreads were of searches scoring zero.

`pgstore`'s second-account test narrows the cause. Through the parent index,
a second account's rows score with the `default` partition's term
statistics. A term `default` does not hold scores zero, and every other term
scores with the wrong library's statistics.

**The parent index name also costs the walk.** With a second partition, a
walk over one partition's index rechecks each row against the named parent
at about 30 µs a row. Naming the partition's own index, with the partition
fixed at plan time, took the 200-document walk from 98 ms to 5 ms. Naming it
also fixes the zero scores, but needs the leaf index's name at search time
and `SELECT` on `chunks_default` for the app role. Search now names it, and
migration 17 grants the `SELECT`.

**The scan cap applies to both shapes alike.** Upstream issue 516 reports
that 1.4.0 ends an ordered scan silently after 100,000 index entries. The
per-document queries and the single walk read the same ordering, so both
stop at the same point.
