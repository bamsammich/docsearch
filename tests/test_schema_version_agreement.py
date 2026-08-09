"""Go and Python must agree on the schema version.

The number is chosen once, in ``docsearch.db.SCHEMA_VERSION``. The Go server
asserts its own copy at readiness, so a bump on one side alone does not fail
the build -- it ships a binary that refuses every database it was meant to
serve. This is the check that turns that into a test failure.

The schema *itself* needs no such check: both languages read
``python/docsearch/schema.sql``.
"""

from __future__ import annotations

import re
from pathlib import Path

from docsearch.db import SCHEMA_VERSION

STORE_GO = Path(__file__).resolve().parents[1] / "internal/store/store.go"
_CONST = re.compile(r"^const RequiredSchemaVersion = (\d+)$", re.MULTILINE)


def test_go_requires_the_version_python_writes() -> None:
    match = _CONST.search(STORE_GO.read_text())
    assert match, f"no `const RequiredSchemaVersion` found in {STORE_GO}"
    assert int(match.group(1)) == SCHEMA_VERSION, (
        f"internal/store/store.go requires schema version {match.group(1)} but "
        f"docsearch.db.SCHEMA_VERSION is {SCHEMA_VERSION}. The server asserts its "
        f"constant at readiness, so a mismatch refuses every database at deploy "
        f"time rather than failing here."
    )
