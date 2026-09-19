# Postgres search spike: pass, with pg_textsearch

Date: 2026-09-19 · Engines: `pg_textsearch` 1.4.0 on CloudNativePG's
PostgreSQL 17.11 image; ParadeDB `pg_search` 0.25.9 on PostgreSQL 18.6;
built-in `ts_rank` · Host: Apple Silicon, arm64
Method: copy the live FTS5 index (20 documents, 2,096 chunks) into Postgres
under a user ID, then run the committed labelled query set and the self-label
probe through each engine with one scoring harness. The FTS5 baseline goes
through `store.Search` itself. Code in `spike/postgres/`;
`spike/postgres/run.sh` reproduces every figure here in about ten minutes.

## Verdict: Postgres can replace FTS5, using pg_textsearch

| engine | labelled @1 / @3 / @8 / @20 | self-label @1 / @3 | median scoped query |
|---|---|---|---|
| FTS5, today | 40 / 55 / 68 / 77% | 99 / 100% | 5.6 ms |
| `pg_textsearch`, one field | 40 / 57 / 68 / 79% | 96 / 98% | 13.4 ms |
| ParadeDB, one field | 38 / 53 / 68 / 77% | 98 / 100% | 2.9 ms |
| built-in `ts_rank` | 42 / 53 / 66 / 74% | 92 / 97% | 3.8 ms |

The labelled set has 53 queries, each scoped to its target document. The
self-label probe asks 807 queries built from chunks' own words.

`pg_textsearch` matches or beats FTS5 at every labelled depth and trails by
three points on self-label @1. Three reasons put it ahead of ParadeDB:

| | `pg_textsearch` | ParadeDB |
|---|---|---|
| license | PostgreSQL | AGPL-3.0 |
| CloudNativePG | installs into the operator's own PostgreSQL 17 image from a prebuilt `.deb` | its own image; operator compatibility untested |
| PostgreSQL 17 | supported | image ships 18 |
| speed | slower, see [Latency](#latency) | faster |

ParadeDB is the fallback if latency becomes the constraint. The built-in
`ts_rank` needs no extension at all and lands within a few points; it is
the floor, not a candidate, because it is not BM25.

## One field, with the heading written twice

Indexing heading and body separately and adding the two scores cost
`pg_textsearch` 6 points at @8 (38 / 53 / 62 / 81%) and 8 points of
self-label @1 (91%). ParadeDB with two boosted fields lost the same way
(38 / 53 / 64 / 75%, self-label 93%).

FTS5's `bm25(chunks_fts, 1.0, 2.0)` is BM25F: it adds weighted term counts
across columns *before* saturation, under one IDF and one document length.
Two indexes score each column separately, so a term in both the heading and
the body counts twice at full strength. On one labelled query, an acronym sits
in the heading of every chunk in its section and in some bodies; it outscored
the rare body word that identifies the right chunk, and the target fell from
rank 1 to rank 4.

One expression index over `heading_path || ' ' || heading_path || ' ' || text`
weights the heading 2x inside term frequency, as FTS5 does, and closed the
gap on both engines. ParadeDB requires a tokenizer cast with an alias on an
indexed expression (`::pdb.unicode_words('alias=body')`).

## Users: partition by user, and let row-level security filter

A second user ("bob", three copies of one manual) loaded into one
shared index moved alice's results, because BM25's IDF and average length
span every row in the index:

A changed result is a labelled query whose hit position moved, or a
self-label query that entered or left the misses.

| layout | alice's results changed by bob's load |
|---|---|
| `pg_textsearch`, shared table | 14 |
| ParadeDB, shared table | 12, labelled @1 38% to 36% |
| `pg_textsearch`, `PARTITION BY LIST (user_id)` | 0 |
| ParadeDB, `PARTITION BY LIST (user_id)` | 0 |

A shared index is also a small side channel: score shifts reveal something
about what other users hold. Each partition gets its own search index, so its
statistics cover one user's rows.

Row-level security held in all four layouts, run as a role that owns nothing
and lacks `BYPASSRLS`, with `FORCE ROW LEVEL SECURITY` and a policy of
`user_id = current_setting('app.user_id', true)`:

| check | result |
|---|---|
| no user set | sees zero rows |
| user set, no `user_id` filter in the query | sees only its own rows |
| index search with no `user_id` filter | a full page of 20, all its own |
| insert as the app role | permission denied |

On the partitioned tables the policy also prunes: alice's search scans only
`chunks_alice`'s index. Row-level security guards against a forgotten
filter, not against a compromised application, which can set any user ID.

## Unscoped search stays a per-document loop

Unscoped search ranks within each document and interleaves by rank. It can be
one statement, a `LATERAL` join over documents keeping the store's cap of 4k
candidates per document, and it returns identical results on 53 of 54
queries; the 54th swaps two tied results. It is not faster:

| engine | per-document loop | one statement |
|---|---|---|
| `pg_textsearch`, one field | 114 ms | 138 ms |
| ParadeDB, one field | 29 ms | 498 ms |

A first version that scored every matching row in the corpus was slower
still and agreed on only 51 and 44 queries, because boosting applied beyond
the per-document cap.

## Latency

`pg_textsearch` orders rows through its index in a fraction of a millisecond,
but a score in the select list is computed standalone, at about 150 µs a row.
The store needs scores for its index-term boost and keyword-reference
penalty, so a scoped query pays for its 80 candidates: 13.4 ms. Scoring
every row outside the index, as the first two-index version did, took 159 ms.

## Rules for the port

1. Index one field: heading twice, then body.
2. Partition chunks by user, `PARTITION BY LIST (user_id)`, one partition per
   user. One partition each suits a deployment with a handful of users;
   thousands of partitions is a different design.
3. Row-level security on every user table: `FORCE ROW LEVEL SECURITY`, an
   app role that owns nothing and lacks `BYPASSRLS`, and `SET LOCAL
   app.user_id` at the start of each transaction.
4. Strip NUL from chunk text at ingest. Postgres `text` cannot hold it, FTS5
   stores it silently, and one chunk in the live corpus carries one.
5. Keep the candidate cap small with `pg_textsearch`, since every candidate
   costs a standalone score.
6. If ParadeDB is ever used: set `plan_cache_mode = force_custom_plan`. pgx
   caches prepared statements, Postgres switches one to a generic plan on
   its sixth execution, and ParadeDB's `|||` then fails with "right-hand side
   must be a text value".
7. Create databases as UTF-8. `initdb` without options picked `SQL_ASCII` on
   CloudNativePG's image; the operator itself creates UTF-8.

## Not measured

- **The operator.** The image was built and run under plain Docker. Loading
  `pg_textsearch` through a Cluster's `postgresql.shared_preload_libraries`
  under the CloudNativePG operator is untested.
- **Ingest-time index cost.** Indexes were built once after bulk loads
  (0.1 to 0.5 s here); inserting while searching was not measured.
- **Scale.** 2,096 chunks for one user and 4,928 across two. Standalone
  scoring cost grows with candidates, not corpus size, but nothing larger was
  run.
