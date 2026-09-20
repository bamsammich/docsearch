#!/usr/bin/env bash
# Fails when any generated file is out of date with what it is generated
# from.
#
# Regenerates everything and compares the result with what was on disk
# beforehand. Comparing against HEAD instead would be wrong on a branch that
# changes generated output on purpose: a proto edited and regenerated in the
# same commit differs from HEAD for a good reason, and a check that cannot
# tell the two apart fails every such branch.
set -euo pipefail

generated() {
  find internal/api internal/schema/migrations internal/store/dbgen \
    -type f \( -name '*.go' -o -name '*.sql' \) 2>/dev/null | sort
  find . -path ./.venv -prune -o -type d -name mocks -print 2>/dev/null |
    while read -r dir; do find "$dir" -type f -name '*.go' | sort; done
  echo internal/schema/schema.snapshot.sql
}

fingerprint() {
  generated | xargs shasum 2>/dev/null | shasum
}

before=$(fingerprint)
mise run generate >/dev/null
after=$(fingerprint)

if [ "$before" != "$after" ]; then
  echo "generated files are out of date; run 'mise run generate' and commit the result" >&2
  git --no-pager diff --stat -- \
    internal/api internal/schema internal/store/dbgen '**/mocks' >&2
  exit 1
fi
