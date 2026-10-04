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

The store could keep both dialects behind its interface for good. It will not.

The query layer is 45 sqlc queries, and a second permanent dialect doubles
what every later change costs: two files to edit, two generated packages, two
sets of placeholder syntax, and a class of bug where the dialects agree in
tests and diverge on a fixture nobody wrote. The deployment holds no SQLite
file after this phase, so a permanent second dialect would exist only to be
maintained.

Both do live side by side while the phase runs, which step 4a discovered
rather than planned. `internal/schema` is where every other suite builds its
test database, so switching it alone left three packages with no way to make
one, and a step that breaks the tree does not ship. So the migrations are
written once per dialect under `internal/schema/migrations/`, a database is
asked which SQL it speaks, and the packages that read and write an index move
one step at a time. Step 4g deletes the SQLite half with the last of them.

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

It runs in CI rather than locally, which needs Postgres and `pg_textsearch`
there. `timescale/timescaledb-ha:pg17` is published with both, carrying
PostgreSQL 17.11 and `pg_textsearch` 1.4.0, the two versions the spike
measured, and preloading the library already. Tests pull it through
testcontainers, the way the migration tests already reach a database, so
nothing here builds an image and a contributor needs only Docker.

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
| 4a | a Postgres harness: testcontainers helpers over the published image, and `internal/schema` speaking both dialects | the Postgres baseline creates version 5's shape, applied and rolled back against a container with rows in every table it touches; both dialects reach the same version; a database without the extension fails with a message naming it |
| 4b | `users`, `user_id` on every tenant table, `PARTITION BY LIST (user_id)`, and the policies | the row-level-security checks run against the role that owns nothing: a session naming no user sees nothing, a query without a filter sees one library, two libraries holding one `doc_id` stay apart, a write for another user is refused, an update it cannot see changes nothing, and the role cannot reach the schema; the migration moves existing rows into a partition and comes back out |

The two dialects part company here. Postgres reaches version 6 and SQLite
freezes at 5, since multi-user exists only on Postgres, so `VersionFor`
answers per dialect and a new SQLite migration fails the version guard. A
schema change goes to Postgres from here.
| 4c | sqlc on the `postgresql` engine beside the sqlite one, 45 queries ported, `internal/repository/postgres` beside the SQLite writer | all three repository suites against a container; two workers claiming at once, which `FOR UPDATE SKIP LOCKED` makes possible where SQLite's single writer serialised it |

Row-level security changes what a repository may do, and 4c settled three
things because of it. Nothing touches the database outside a transaction that
named its user, single reads included, since a bare statement matches no rows
at all; `session` and the `read` helper over it are the only paths. The user
is bound when a repository is constructed rather than passed per call, which
keeps the service ports unchanged until 4f threads an owner down. Session-level
`SET` was rejected: `database/sql` hands out pooled connections, so a variable
set for one request would still be set when another borrowed that connection.
| 4d | search on `pg_textsearch`, built from one expression index, scoring a scoped query through the per-document loop, and the rest of the read layer beside it | the search suite against a container: the cross-document merge, a scoped query, the relevance transform and the keyword-reference penalty, plus `docsearch-eval` held to the spike's figures, which needs a real library and so is run by hand |

Three things about the query shape came out of 4d, each from an `EXPLAIN`
rather than from reasoning about one. The `ORDER BY` has to repeat the indexed
expression verbatim or the planner ignores the index and scores every row
standalone. Joining `documents` for the title defeats it the same way, so
search reads `chunks` alone and the title and the ready check come from one
lookup per document instead of one per result. And a table small enough to
scan is scanned, which hands back rows that matched nothing, so the outer
query drops a score of zero.

Migration 8 is a correction rather than a feature: migration 6 granted the
five tenant tables to the app role and left `schema_version` out, so the
readiness probe read every database as unversioned. Ranking itself is
untouched -- the store asks `pg_textsearch` for the scores the spike
measured, and nothing here changes how they are compared.
| 4e | the response cache on Postgres, per user, behind the `fetch.Cache` interface it already has, and a stored `robots.txt` that expires | the cache suite against a container, covering refetch, revalidation, isolation between two users, and a restart; the fetcher suite for each class of answer a `robots.txt` request can get |

A crawl cache in a local file is the piece a container takes away. Surviving a
restart is most of what the cache is for: a cancelled crawl resumes through
it, refreshes send conditional requests from it, and re-chunking makes no
requests at all. Migration 9 puts `responses` and `robots` in
the database, under the same policy as everything else.

`robots` is per user as well, although a robots.txt is public and identical
for everybody. Which hosts someone crawled is not public, and one request per
user per host is cheaper than an exception to the isolation rule. Neither
table is partitioned, unlike `chunks`: a cache holds one crawl's responses
rather than a library's lifetime, so a second user needs a `users` row and
nothing else.

Keeping the cache turned up a bug that the move would have made permanent.
Nothing read `fetched_at` on either table, so a `robots.txt` was fetched once
per host and obeyed forever. A container-local file was cleared by every
restart, which kept the copy fresh by accident; in Postgres it survives. RFC
9309 section 2.4 allows a cached copy for at most 24 hours, so `robotsFor`
checks the age and reads the file again past a day.

Expiry forces a second question, because a refresh can fail, and the answer
the code gave was wrong. `fetchRobots` returned an empty body for every
non-200, and an empty `robots.txt` disallows nothing, so a host answering 500
granted the crawler everything. RFC 9309 draws the line between two cases:

| answer | what it means | what the crawler does |
|---|---|---|
| 400-499 | unavailable, section 2.3.1.3 | may access anything |
| 500-599 or a network error | undefined, section 2.3.1.4 | complete disallow, or the stored copy where one exists |

A complete disallow is expressed as `User-agent: *` and `Disallow: /` rather
than as a flag, so one answer flows out of `robotsFor` and the parser stays
the only thing that reads rules. An unreachable host keeps whatever copy is
stored, however old, which the same section permits: one server error should
not stop a crawl the host's own rules allow.

Redirects are still not followed for `robots.txt`, and a redirect is read as
unavailable. Section 2.3.1.2 permits that only past five hops, so the gap is
real; closing it means routing each hop through the guard, which is its own
step. Reading a redirect as undefined instead would refuse every host that
serves the file from somewhere else.

The worker still opens the SQLite cache. `site.Source` opens one from a path
of its own, so switching it means handing it a `fetch.Cache` instead, and the
cache needs an owner that no request carries until 4f. `internal/site/fetch/pgcache`
is complete and tested ahead of the wiring that 4f rewrites anyway.
| 4f | a user on every request, and both binaries on Postgres: the doors resolve an owner and ask for that user's services, and the worker claims one library's queue | a refusal case per door for a request that named no user, plus the end-to-end suite rebuilt on a container |
| 4g | the isolation test in CI, and SQLite out of the tree: the driver, the FTS5 schema, the SQLite migrations and the remaining file paths go | the isolation test as described, plus a deployment that starts with no SQLite file present |

4f took the Postgres switch that 4g was going to make. Threading an owner
through a door that then reads SQLite is theatre, because the SQLite store
has no user to thread it to, so the two had to happen together. 4g keeps the
deletion: `internal/store`, `internal/repository/sqlite`, the SQLite
migrations, `scripts/docsearch-db`, which opens a volume the compose file no
longer mounts, and `docsearch-eval`, which still reads a file.

The owner travels in the request context. `internal/owner` puts it there and
`owner.From` takes it out, refusing where nothing named one. A parameter
would have been plainer, and the MCP door rules it out: a tool handler
receives a context and its decoded arguments, with no access to the request,
and one mechanism has to serve both doors.

What a door holds is a factory rather than a service. A service is built for
one user, the server is built before any request arrives, and the repository
already took its user at construction, which 4c chose for this reason.
`internal/library` is the only place that builds one, so adding a dependency
to a door does not spread knowledge of multi-tenancy across the service
layer, which still knows nothing about it.

| decision | why |
|---|---|
| one worker, one library | a job, the document it produces and the responses it crawled belong to the same user; `--user` names which, defaulting to the built-in one |
| `site.Source` takes a cache | it used to open one from a path, and a crawl now reads the asking user's |
| `--dsn` replaces `--db` | the server and the worker address a database rather than a file; `migrate` takes either while both dialects live |
| `library.Ready` takes no user | the readiness probe answers before anyone has signed in, and neither table it reads carries a policy |

The end-to-end suite runs against a container now, which is what makes the
switch a tested claim rather than a compiling one. Two of its assertions
changed because a Postgres column is typed: `permanent` reads back as `true`
where SQLite stored `1`.

## What the code assumes, and what a deployment provides

Getting `pg_textsearch` into a cluster is deployment work, and nothing in
this repository does it. What the code owes in return is a clear boundary,
because two of the three statements the design rests on need privileges the
code will not have. Each was checked against
`timescale/timescaledb-ha:pg17`.

| statement | privilege | who does it |
|---|---|---|
| `CREATE EXTENSION pg_textsearch` | superuser, and the hint says so | the cluster, before docsearch connects |
| `CREATE ROLE` for the restricted app role | `CREATEROLE` | the cluster |
| the partitioned table, the BM25 index, `FORCE ROW LEVEL SECURITY` and the policies | plain ownership of the database | a migration |

CloudNativePG runs its `app` user without superuser, so a migration that
tried to create the extension would fail on the one deployment that matters.
A migration therefore assumes `pg_textsearch` is present, and names it when
it is absent rather than failing on the first query that needs it. Step 4a
tests that by pointing at a database without it.

Two roles reach the database rather than one: an owner that migrations run
as, and a restricted role that every request runs as, which owns nothing and
lacks `BYPASSRLS`. Both arrive as configuration.

How the extension reaches the cluster is the operator's problem, and it has
more than one answer. Adding it to the image the operator runs is what the
spike built, and never tested under the operator itself. CloudNativePG
1.28's image volumes mount an extension-only image instead, and want
PostgreSQL 18's `extension_control_path`. The code cannot tell the two
apart. If neither works the engine choice reopens, and the alternatives
are ParadeDB, whose licence and image were the reasons it lost, and the
built-in `ts_rank`, which is not BM25 and lands a few points lower.

## Not settled by the spike

**Ingest while searching.** The spike built indexes once after bulk loads,
in half a second or less. Inserting chunks while queries run was not
measured, and a worker writing a 1,800-page manual while the server answers
is the normal case here. Step 4d is where a regression would show.

**Scale.** Two users and under 5,000 chunks. Standalone scoring cost grows
with the candidate cap rather than with the corpus, which is the reason to
keep the cap small, but nothing larger was run.
