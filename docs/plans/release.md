# Moving a v1 library across, and releasing v2

Phase 08 of v2. The phase ends when a v1 library migrates into a self-hosted
v2, single-user v2 runs from Compose, and `v2` replaces `main` as `v2.0.0`
while `v1` starts its support window.

## A v1 library is re-read, not copied

`docsearch import-v1` opens a v1 index read-only and queues each finished
document again from the file or site it was read from. v1's chunks came from
the Python pipeline v2 retired, so copying them would put v1's extraction in
a store that is otherwise all v2's. Titles carry across, since a person may
have chosen them. A document v1 never finished is skipped and named, and
`--root-map OLD=NEW` moves a file path from where v1 saw the library to where
v2 does.

The import was run against a real v1 library of 20 documents, behind a v2
started from `deploy/docker/compose.yaml`. 19 were queued and read. The
twentieth had been deleted from the library since v1 indexed it, and v2
refused the path as it should. `docsearch-eval` against the result scored
40/58/68/79% at @1/@3/@8/@20, where v1's FTS5 scored 40/55/68/77%.

## Releasing

| step | what | who |
|---|---|---|
| 8c-1 | `v1` branch, cut from `main` at `v1.0.0` | done |
| 8c-2 | pull request from `v2` into `main` | opened; `main` needs an approving review, so the maintainer merges it |
| 8c-3 | signed tag `v2.0.0` on the merged commit | the maintainer, whose key signs tags |
| 8c-4 | `v1`'s README names its support window and points to `main` | after 8c-2, so it never claims a release that has not happened |

`v1` takes security fixes only, until six months after `v2.0.0`.

## The module path stays

Go refuses a `v2.0.0` tag on a module whose path lacks `/v2`, so `go install
github.com/bamsammich/docsearch/cmd/docsearch@latest` keeps resolving to
`v1.0.0`. docsearch is an application built from a checkout or from its
container images, not a library imported by path, and the README documents
only those routes. Renaming the module would rewrite the import of nearly
every Go file to fix an install path nobody is told to use. The README says
so instead.
