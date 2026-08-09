"""The Go store tests must run against the schema this build defines."""

from __future__ import annotations

from pathlib import Path

from docsearch.db import SCHEMA

FIXTURE = Path(__file__).resolve().parents[1] / "internal/store/testdata/schema.sql"


def test_go_fixture_matches_the_python_schema() -> None:
    """A drifted fixture makes the Go suite green against a schema nobody ships.

    The Go tests skip rather than fail when this file cannot be read, so drift
    would otherwise surface as a passing run that exercised nothing.
    """
    assert FIXTURE.exists(), f"{FIXTURE} is missing; run scripts/sync_go_schema_fixture.py"
    assert FIXTURE.read_text() == SCHEMA, (
        "internal/store/testdata/schema.sql no longer matches docsearch.db.SCHEMA. "
        "Run scripts/sync_go_schema_fixture.py and commit the result."
    )
