"""Write the Go store's schema fixture from the schema Python defines.

The Go tests build their index against a checked-in copy of the DDL. Nothing
made that copy follow ``db.SCHEMA``, so a column added on the Python side left
the Go tests running against the previous schema -- and because the fixture is
loaded with ``t.Skipf`` when absent, a broken copy degrades to skipped tests
rather than failing ones.

Run this after changing ``db.SCHEMA``. ``test_go_schema_fixture.py`` fails when
the checked-in copy and ``db.SCHEMA`` disagree, so forgetting is caught.
"""

from __future__ import annotations

from pathlib import Path

from docsearch.db import SCHEMA

FIXTURE = Path(__file__).resolve().parents[1] / "internal/store/testdata/schema.sql"


def main() -> None:
    FIXTURE.write_text(SCHEMA)
    print(f"wrote {FIXTURE}")


if __name__ == "__main__":
    main()
