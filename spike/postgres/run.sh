#!/bin/sh
# Reproduce the Postgres search spike from a copy of the live FTS5 index.
#
#   spike/postgres/run.sh
#
# Run from the repository root, with the docsearch stack up so its database
# can be copied out. Starts two throwaway Postgres containers bound to
# 127.0.0.1, loads the corpus into each engine, and prints every figure the
# write-up in docs/research/postgres-spike.md quotes. Outputs land in
# var/spike2/, which is ignored. About ten minutes, most of it the two-index
# pg_textsearch engine, which scores every row standalone.
set -eu

out=var/spike2
mkdir -p "$out"
scripts/docsearch-db --backup "$out/corpus.db"
cp "$out/corpus.db" "$out/fts5.db"
go build -o "$out/pg-spike" ./spike/postgres

(cd spike/postgres && BUILDX_NO_DEFAULT_ATTESTATIONS=1 \
  docker build -q -f Dockerfile.textsearch -t docsearch-spike-textsearch . > /dev/null)
docker rm -f ds-spike-ts ds-spike-pdb > /dev/null 2>&1 || true
docker run -d --name ds-spike-ts -p 127.0.0.1:55431:5432 docsearch-spike-textsearch > /dev/null
docker run -d --name ds-spike-pdb -p 127.0.0.1:55432:5432 \
  -e POSTGRES_HOST_AUTH_METHOD=trust paradedb/paradedb:0.25.9 > /dev/null
sleep 10

ts() { echo "postgres://postgres@127.0.0.1:55431/$1?sslmode=disable"; }
pdb() { echo "postgres://postgres@127.0.0.1:55432/$1?sslmode=disable"; }
for db in ts tsf tsfp native; do docker exec ds-spike-ts psql -U postgres -qc "CREATE DATABASE $db"; done
for db in pdb pdbf pdbfp; do docker exec ds-spike-pdb psql -U postgres -qc "CREATE DATABASE $db"; done

run() { "$out/pg-spike" "$@"; }

echo "== quality and latency, one user"
run -engine fts5 eval
run -engine textsearch -pg "$(ts ts)" load > /dev/null && run -engine textsearch -pg "$(ts ts)" eval
run -engine textsearchf -pg "$(ts tsf)" load > /dev/null && run -engine textsearchf -pg "$(ts tsf)" eval
run -engine paradedb -pg "$(pdb pdb)" load > /dev/null && run -engine paradedb -pg "$(pdb pdb)" eval
run -engine paradedbf -pg "$(pdb pdbf)" load > /dev/null && run -engine paradedbf -pg "$(pdb pdbf)" eval
run -engine native -pg "$(ts native)" load > /dev/null && run -engine native -pg "$(ts native)" eval

echo "== unscoped search: one statement against the per-document loop"
run -engine textsearchf -pg "$(ts tsf)" roundrobin
run -engine paradedbf -pg "$(pdb pdbf)" roundrobin

echo "== a second user's documents: shared index, then partitioned by user"
isolate() { # engine url partition-flag label
  run -engine "$1" -pg "$2" $3 load > /dev/null
  run -engine "$1" -pg "$2" -v eval > "$out/alice-before-$4.txt"
  run -engine "$1" -pg "$2" $3 -user bob -only manual-of-ma-lighting-international-gmbh -times 3 load > /dev/null
  run -engine "$1" -pg "$2" -v eval > "$out/alice-after-$4.txt"
  changed=$(diff "$out/alice-before-$4.txt" "$out/alice-after-$4.txt" | grep '^>' | grep -vc median || true)
  echo "$4: alice's per-query results changed by bob's load: $changed"
  run -engine "$1" -pg "$2" rls | grep -E "PASS|FAIL"
}
isolate textsearchf "$(ts tsf)" "" textsearch-shared
isolate textsearchf "$(ts tsfp)" -partition textsearch-partitioned
isolate paradedbf "$(pdb pdbf)" "" paradedb-shared
isolate paradedbf "$(pdb pdbfp)" -partition paradedb-partitioned
