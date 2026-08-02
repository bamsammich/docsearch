"""Precision of section -> chunk resolution.

Coverage metrics cannot police this. Over-matching raises the count of
resolved joins, so an "unjoinable = 0" check reports improvement while the
answers get worse. These tests assert what must *not* match.
"""

from __future__ import annotations

import sqlite3

import pytest

from docsearch.db import chunks_in_section

SECTIONS = ["4", "4.1", "4.2", "4.10", "41", "41.11", "43", "43.6", "43.6.1", "5"]


@pytest.fixture
def populated(conn: sqlite3.Connection) -> sqlite3.Connection:
    conn.execute(
        "INSERT INTO documents (doc_id, title, format, source_path, sha256, status)"
        " VALUES ('d', 't', 'pdf', '/x', 'h', 'ready')"
    )
    for i, sec in enumerate(SECTIONS):
        conn.execute(
            "INSERT INTO chunks (doc_id, ordinal, section, heading_path, text)"
            " VALUES ('d', ?, ?, ?, ?)",
            (i, sec, f"{sec}. Heading", f"body of section {sec}"),
        )
    return conn


def _sections_for(conn: sqlite3.Connection, section: str) -> list[str]:
    ids = chunks_in_section(conn, "d", section)
    rows = conn.execute(
        f"SELECT section FROM chunks WHERE id IN ({','.join('?' * len(ids))}) ORDER BY ordinal",
        ids,
    ).fetchall()
    return [r["section"] for r in rows]


def test_chapter_reference_covers_only_its_own_subtree(populated: sqlite3.Connection) -> None:
    assert _sections_for(populated, "4") == ["4", "4.1", "4.2", "4.10"]


def test_chapter_reference_does_not_reach_numerically_similar_chapters(
    populated: sqlite3.Connection,
) -> None:
    """A term pointing at chapter 4 must resolve to zero chunks in 41 or 43."""
    resolved = _sections_for(populated, "4")
    assert "41" not in resolved
    assert "41.11" not in resolved
    assert "43" not in resolved
    assert "43.6" not in resolved
    assert "43.6.1" not in resolved


def test_deep_reference_is_exact(populated: sqlite3.Connection) -> None:
    assert _sections_for(populated, "43.6") == ["43.6", "43.6.1"]
    assert _sections_for(populated, "43.6.1") == ["43.6.1"]


def test_two_digit_chapter_does_not_absorb_its_own_prefix(
    populated: sqlite3.Connection,
) -> None:
    assert _sections_for(populated, "41") == ["41", "41.11"]


def test_leaf_reference_resolves_to_itself_only(populated: sqlite3.Connection) -> None:
    assert _sections_for(populated, "5") == ["5"]
    assert _sections_for(populated, "4.1") == ["4.1"]
