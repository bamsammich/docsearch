# Moving storage to Postgres, one library per user

Phase 04 of v2. Every row gains an owner, the database enforces that owning,
and both stores move from SQLite to Postgres: the search index and the
crawler's response cache. The phase ends when a test calls every tool as one
user with another user's identifiers and gets nothing back, and when a
deployment holds no SQLite file.

Users arrive here without a way to sign in as one, since authentication is
phase 05. Every request belongs to one built-in user, which
`none` and `token` will own once auth modes exist, and phase 05 attaches real
identities to the same table. Isolation is built and tested
first because retrofitting it under an auth layer means auditing every query
twice.

`docs/research/postgres-spike.md` settled the engine and the layout, and its
seven rules are requirements here rather than suggestions. The spike copied a
live FTS5 index into Postgres and scored both with one harness; this phase
makes the Go worker write Postgres directly and holds retrieval to the same
numbers.

## Postgres replaces SQLite rather than joining it

The store could keep both dialects behind its interface. It will not.

The query layer is 45 sqlc queries, and a second dialect doubles what every
later change costs: two files to edit, two generated packages, two sets of
placeholder syntax, and a class of bug where the dialects agree in tests and
diverge on a fixture nobody wrote. The deployment holds no SQLite file after
this phase, so the second dialect would exist only to be maintained.

What stays is narrower. A v1 index is a SQLite file someone is still running,
and phase 08 ships a command that reads one and re-ingests each document from
its recorded source. That reader wants `modernc.org/sqlite` and the v1 table
shapes, and wants neither migrations nor the query layer, so it lands in phase
08 as a read-only package rather than as a second backend here.

| today | after phase 04 |
|---|---|
| `internal/repository/sqlite` writes the index | `internal/repository/postgres` writes it |
| `internal/store` reads SQLite with FTS5 | reads Postgres with `pg_textsearch` |
| `internal/schema` carries SQLite migrations | carries Postgres migrations |
| `internal/site/fetch` caches responses in a file | caches them in Postgres |
| a v1 SQLite file is the deployment | a v1 SQLite file is an import source, in phase 08 |

## What the spike decided, and what this phase must therefore do

| rule from the spike | what it means here |
|---|---|
| index one field, heading written twice then body | one expression index over `heading_path \|\| ' ' \|\| heading_path \|\| ' ' \|\| text`, not one index per column |
| partition chunks by user | `PARTITION BY LIST (user_id)`, one partition per user, created when a user is |
| row-level security everywhere | `FORCE ROW LEVEL SECURITY`, an app role owning nothing and lacking `BYPASSRLS`, `SET LOCAL app.user_id` opening every transaction |
| strip NUL at ingest | Postgres `text` cannot hold it and one chunk in a real corpus carries one |
| keep the candidate cap small | every candidate costs a standalone score of about 150 µs |
| UTF-8 databases | `initdb` with no options chose `SQL_ASCII` on the operator's image |
| `plan_cache_mode` only matters for ParadeDB | not adopted, so not configured |

Two scoring columns cost six points at depth 8 because a term in a heading
and a body counts twice at full strength, while FTS5 adds weighted counts
before saturation. The one-field index reproduces FTS5's arithmetic, which is
why it is a requirement and not a preference.

Unscoped search stays a loop over documents. One `LATERAL` statement returned
the same results and was slower on both engines, so the store keeps the shape
it has.

## Isolation is a test, not a claim

Row-level security guards a query that forgot its filter. It does not guard a
compromised application, which can set any user ID, so the isolation test is
what makes the claim.

The test drives the real API surface, both doors, as two users: it ingests
distinct documents for each, then calls every tool and every RPC as the
second user passing the first user's `doc_id`, `job_id` and section values. A pass
returns no rows and changes none, answering the same way whether the
identifier exists or not, so a probe cannot tell a document that belongs to
someone else from one that was never there.

It runs in CI rather than locally, which needs Postgres and
`pg_textsearch` there. No public Postgres image carries the
extension, so what the spike built moves out of `spike/postgres` into the
repository proper, and CI builds it. Tests reach it through testcontainers, the way the
migration tests already reach a database.

## Users exist before sign-in does

A `users` table holds an internal identifier and nothing else that matters
yet. Phase 05 adds an issuer and a subject, which is what keeps two identity
providers from colliding on one account, and phase 04 creates one row.

`user_id` goes on every tenant table, and Postgres requires the partition key
inside the primary key of a partitioned table, so `chunks` is keyed on
`(user_id, id)`. Creating a user creates a partition, which is a
write no request path performs: one partition per user suits a deployment
with a handful of them, and a deployment with thousands needs a different
design than the spike measured.

Nothing is shared between users, including extractions of the same URL. A
fetchable URL is not proof that a document is public, and a shared row is a
component one user's upload can reach another user through. The response
cache is per-user for the same reason, which costs a second crawl of a site
two users both want and buys an absence of cross-user paths.

## Steps

Each step is one pull request. A step lands only with the Postgres tests that
prove it, and never leaves the tree unable to build an index end to end.

| step | what lands | how it is checked |
|---|---|---|
| 4a | a Postgres harness: the `pg_textsearch` image promoted out of `spike/`, testcontainers helpers, and `internal/schema` speaking goose's Postgres dialect | the first migration creates today's shape in Postgres, applied and rolled back against a container with rows in every table it touches |
| 4b | `users`, `user_id` on every tenant table, `PARTITION BY LIST (user_id)`, the app role, and the policies | the spike's four row-level-security checks, run against a role that owns nothing: no user set sees nothing, a query without a filter sees one user's rows, an index search without a filter sees one user's rows, and an insert as the app role is refused |
| 4c | sqlc on the `postgresql` engine, 45 queries ported, `internal/repository/postgres` in place of the SQLite writer | the repository suite, moved over and run against a container; `FOR UPDATE SKIP LOCKED` replaces the single-writer claim, so two workers claiming at once is now a case worth writing |
| 4d | search on `pg_textsearch`, built from one expression index, scoring a scoped query through the per-document loop, with NUL stripped at ingest | `docsearch-eval` on a Postgres index the Go worker wrote, held to the spike's labelled and self-label figures |
| 4e | the response cache on Postgres, per user, behind the `fetch.Cache` interface it already has | the cache suite against a container, and a crawl resumed after the process that started it exits |
| 4f | a user on every request: the service layer takes an owner, both API doors supply the built-in one, and every transaction opens with `SET LOCAL app.user_id` | a unit test per service that a call without an owner is refused rather than defaulted |
| 4g | the isolation test in CI, and SQLite out of the deployment: the driver, the FTS5 schema and the file paths go | the isolation test as described, plus a deployment that starts with no SQLite file present |

## Not settled by the spike

**The operator.** `pg_textsearch` was built into the operator's own
PostgreSQL 17 image and run under plain Docker. Loading it through a
cluster's `shared_preload_libraries` under the operator itself is untested.
The port does not depend on the answer, since tests run the image directly,
but a deployment does. Check it before phase 04 ends, and if it fails the
choice is ParadeDB, whose licence and image were the reasons it lost, or the
built-in `ts_rank`, which is not BM25 and lands a few points lower.

**Ingest while searching.** The spike built indexes once after bulk loads,
in half a second or less. Inserting chunks while queries run was not
measured, and a worker writing a 1,800-page manual while the server answers
is the normal case here. Step 4d is where a regression would show.

**Scale.** Two users and under 5,000 chunks. Standalone scoring cost grows
with the candidate cap rather than with the corpus, which is the reason to
keep the cap small, but nothing larger was run.
