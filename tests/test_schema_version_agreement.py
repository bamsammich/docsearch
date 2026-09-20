"""Go and Python must agree on the schema version.

The number is chosen twice while both pipelines are in the tree, in
``docsearch.db.SCHEMA_VERSION`` and in ``internal/schema``. The server asserts
its own copy at readiness, so a bump on one side alone does not fail the build
-- it ships a binary that refuses every database it was meant to serve. This
is the check that turns that into a test failure.

Go reads ``internal/schema/schema.sql``, a copy ``mise run generate`` refreshes
and ``internal/schema`` tests against the original; the copy goes away with
this package.
"""

from __future__ import annotations

import re
from pathlib import Path

from docsearch.db import SCHEMA_VERSION

SCHEMA_GO = Path(__file__).resolve().parents[1] / "internal/schema/schema.go"
_CONST = re.compile(r"^const Version = (\d+)$", re.MULTILINE)


def test_go_requires_the_version_python_writes() -> None:
    match = _CONST.search(SCHEMA_GO.read_text())
    assert match, f"no `const Version` found in {SCHEMA_GO}"
    assert int(match.group(1)) == SCHEMA_VERSION, (
        f"internal/schema requires schema version {match.group(1)} but "
        f"docsearch.db.SCHEMA_VERSION is {SCHEMA_VERSION}. The server asserts its "
        f"constant at readiness, so a mismatch refuses every database at deploy "
        f"time rather than failing here."
    )
