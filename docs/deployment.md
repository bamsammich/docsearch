# Deployment

The processes docsearch runs, schema migrations, service units, Docker, Kubernetes, upgrading from v1 and backups. Back to the [README](../README.md).

## Overview

Local document search over an MCP server. Documents are ingested into a
Postgres database by a background worker; a Go server reads that database and
exposes search, navigation and retrieval tools over MCP Streamable HTTP.

No vector database, no embeddings, no external services. BM25 over
`pg_textsearch`.
[An embedding reranker was measured and rejected](research/embedding-rerank-probe.md).

## Versions

**This is v2:** a Go rewrite with multi-user storage on Postgres, pluggable
authentication, uploads and a Paperless-ngx sync. v1 lives on the
[`v1` branch](https://github.com/bamsammich/docsearch/tree/v1) and takes
security fixes only, until six months after `v2.0.0`.
[Upgrading from v1](#upgrading-from-v1) moves a v1 library across.

docsearch is built from a checkout or from its container images. Its module
path carries no `/v2`, so `go install ...@latest` still resolves to `v1.0.0`.

## Three processes

| Process | Lifetime | Role |
|---|---|---|
| Ingest worker | daemon | claims queued jobs, extracts, chunks, writes |
| MCP server | daemon | reads the database, enqueues jobs, reports status |
| CLI | one-shot | manual ingest, listing, removal, verification |
| Paperless sync | one-shot or daemon, optional | pushes what a Paperless-ngx user can see; runs beside Paperless |

Ingest never runs inside the server process: extraction saturates a core for
minutes and would starve request handling.

## Schema migrations

`docsearch migrate` is idempotent and is the only thing that writes the schema
version. Everything else refuses a database it does not match and says what to
run — opening is not upgrading, because every read path opens the database and
one of them running an older build would otherwise stamp a newer database
backwards.

A database *newer* than the build is refused outright rather than downgraded.
An older build cannot know what a newer one changed, so writing through it
risks corrupting data it does not understand.

The version is recorded only after the schema it describes is confirmed
present. A stamp written without that check is a claim that cannot be false,
and the readiness gate would then pass a database whose queries fail.

Run it as a startup precondition of the **worker**, which is the sole writer:

| | |
|---|---|
| systemd | `ExecStartPre=` in `deploy/systemd/docsearch-worker.service` |
| launchd | `migrate && exec worker` in the agent's shell wrapper — launchd has no `ExecStartPre` |
| Kubernetes | an `initContainer` in `deploy/k8s/deployment.yaml` |

Same command everywhere, run before anything serves. A migration that cannot
be applied exits nonzero and the worker does not start. A worker writing
through a schema it does not match is worse than a worker that is down.

Not every schema change can be applied in place. The change from page
references to section references in `index_terms` is not recoverable without
the source documents, so migration refuses it and says to re-ingest. **The
index is fully regenerable**, which is what makes refusing the right answer.

## Running the worker

```bash
docsearch-worker --dsn "$DOCSEARCH_DSN" --root ~/Documents/library
```

One worker serves every library. A queue is the jobs one person queued in
one library, and the worker takes a job from each waiting queue in turn, so
one queue's long backlog cannot starve another and an account created by a
sign-up is served from its first upload. Each job runs entirely as the person
who queued it: the job, the document it produces and the responses it
crawled all belong to one library, and every write is checked against that
person's grant on it. A job whose requester can no longer add to the library
fails with a message saying so. `--user` pins a worker to the jobs one
person queued in their own library instead.

Jobs are claimed under a lease. A worker killed mid-job leaves an expired
lease, and the next worker reclaims and restarts the job with no intervention.
Transient failures retry up to three times; deterministic ones (unsupported
format, corrupt file, failed structure validation) fail immediately and say so
rather than burning the budget.

A systemd user unit is in `deploy/systemd/`. Its `TimeoutStopSec=900` exists so
an in-flight job can checkpoint or roll back instead of being killed mid-write.

## Running as a service

**Linux:** `deploy/systemd/` — two user units.

**macOS:** `deploy/launchd/` — two user agents. Install with:

```bash
for f in server worker; do
  sed "s|__HOME__|$HOME|g" deploy/launchd/com.bamsammich.docsearch-$f.plist \
    > ~/Library/LaunchAgents/com.bamsammich.docsearch-$f.plist
  launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.bamsammich.docsearch-$f.plist
done
```

Both set `RunAtLoad` and `KeepAlive`, so they start at login and respawn if
killed. Logs go to `~/Library/Logs/docsearch/`.

launchd has no `EnvironmentFile`, so both agents source the token and the
DSNs from their 0600 file in a shell wrapper. They stay out of the plists and
out of `launchctl print` output. The file holds two DSNs: `DOCSEARCH_DSN` for
the request role, which the server and the worker use, and
`DOCSEARCH_OWNER_DSN` for the owner, which only `docsearch migrate` and
`docsearch account` read. A bearer token must be at least 32 characters;
`openssl rand -base64 48` makes one. The agents expect a Postgres
reachable at that DSN; `deploy/docker/compose.yaml` is the simpler route on
macOS, since it brings its own.

### Docker, on the same machine

Two compose files run the same stack. `compose.yaml` at the repository root
pulls the release images from GHCR and needs nothing but a `.env` file beside
it, which is the route the README quickstart takes. The rest of this section
covers `deploy/docker/compose.yaml`, for running a checkout's own code.

`deploy/docker/compose.yaml` runs the same two processes as containers built
from the checkout, and replaces the agents above rather than joining them —
both bind `127.0.0.1:8765` and both write the same database, so run one or the
other.

```bash
scripts/docsearch-up          # rebuild and roll forward
scripts/docsearch-up --logs   # the same, then follow the logs
```

That one command is the whole update story. `up -d --build` rebuilds both
images and recreates a container only when its image digest actually changed,
so running it after a merge that touched neither tree costs a cache-hit build
and leaves the containers alone. Run it after every merge; it is safe to run
when nothing changed. `restart: unless-stopped` plus Docker Desktop's
start-at-login brings the stack back after a reboot.

Two things about this shape are load-bearing:

**The library is bind-mounted at the same absolute path it has on the host,**
and `--root` is that same path. `add_document` takes a filesystem path from the
MCP client, which is a process on the *host*; mounting the library at
`/library` would reject every path a client can actually name.

**Postgres runs as its own service**, on the image that ships `pg_textsearch`
with the library preloaded, and its data lives in a named volume on ext4
inside the Linux VM. `deploy/docker/initdb/00-roles.sql` does the three
things docsearch cannot do for itself: `CREATE EXTENSION` is superuser-only,
`CREATE ROLE` needs `CREATEROLE`, and the database has to exist before
anything can be migrated into it. Postgres runs that file once, on an empty
data directory.

Two roles, because two jobs need different privileges. `docsearch_owner`
owns every table and runs migrations; `docsearch_app` owns nothing and
reaches rows only through the policies, which is what a request runs as.
Neither is a superuser and neither may bypass row-level security, so the
owner is bound by the policies too.

Migration runs in its own service for the reason the systemd unit runs it
from `ExecStartPre`. Both real services gate on
`service_completed_successfully`, so a migration that cannot be applied
means neither ever starts.

No client configuration changes: the container publishes the same
`127.0.0.1:8765` and reads the same token file, so the bridge in
`deploy/client/` keeps working untouched.

## Kubernetes

`deploy/k8s/` holds one Deployment, `replicas: 1`, `strategy: Recreate`. One
worker serves one library, so a second replica would claim the same queue;
`FOR UPDATE SKIP LOCKED` makes two workers safe, which turns the replica
count into a throughput decision rather than a correctness one.

Server and worker are two containers in one Pod because they share the
library volume, and an RWO claim attaches to one node. Exposed over
Tailscale, never an Ingress.

Postgres is operated by the cluster, with its own storage and its own
backups. `deploy/k8s/config.yaml` carries a placeholder secret holding both
connection strings; a cluster operator that already issues Postgres
credentials, such as CloudNativePG, generates one of its own, and the
Deployment points at that instead. The database needs `pg_textsearch`, which
only a superuser can install: `docs/plans/postgres-multiuser.md` carries the
full privilege boundary.

`deploy/systemd/` has units for running both processes on a single host. The
code does not know which deployment shape it is in.

## Upgrading from v1

v1 kept its index in a SQLite file, and v2 keeps it in Postgres. Nothing in
the v1 file is copied across: `docsearch import-v1` reads its list of
documents and queues each one again from the file or site it came from, so
v2's own pipeline reads every document. Titles are kept. A document v1 never
finished is skipped and named, so you can add it by hand once you know why it
failed.

1. Stop v1, and copy its index out of the volume it lives in. The import opens
   the copy read-only, so v1's file is untouched and you can go back to it.
2. Start v2 against the same library directory, then run the import:

   ```
   docsearch import-v1 --dry-run docsearch.db
   docsearch import-v1 docsearch.db
   ```

3. Watch the queue with `docsearch jobs` until every job is `ready`.

v1 recorded each file at the path v1's process saw. If v2 sees the library at
a different path, map one to the other with `--root-map OLD=NEW`, repeated
for each root. A source v2 refuses is reported and the rest are still queued,
and the command exits nonzero so a script notices.

## Backups

**The index is fully regenerable from the library volume.** Every chunk,
search row, page and index term is derived from the source documents by
`docsearch ingest`, and the crawl cache holds responses a crawl can fetch
again. Losing the database costs the time to re-ingest, nothing more.

The volume that needs backing up is **`docsearch-library`**, which holds the
only irreplaceable data. `docsearch-data` can be treated as a cache.
