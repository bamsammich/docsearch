#!/bin/sh
# Stand-in for the CloudNativePG operator: initialise a throwaway cluster and
# run postgres with pg_textsearch preloaded, which the operator would set
# through the Cluster's postgresql.shared_preload_libraries.
set -eu
data=/var/lib/postgresql/data/pgdata
if [ ! -s "$data/PG_VERSION" ]; then
  # UTF-8, as the operator creates databases; initdb alone picks SQL_ASCII.
  initdb -D "$data" --username=postgres --auth=trust --encoding=UTF8 --locale=C.UTF-8 >/dev/null
  # Throwaway spike server, published on 127.0.0.1 only: trust the Docker
  # bridge so the harness can connect from the host.
  echo "host all all 0.0.0.0/0 trust" >> "$data/pg_hba.conf"
fi
exec postgres -D "$data" \
  -c listen_addresses='*' \
  -c shared_preload_libraries=pg_textsearch
