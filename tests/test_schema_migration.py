"""Schema version handling.

The version is a claim that the database matches the code. Both directions of
that claim have to be defended: a read path must not be able to rewrite it, and
it must not be recorded unless the schema it describes is actually present.
"""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from docsearch import db


def _at_version(path: Path, version: int) -> None:
    conn = db.connect(path)
    conn.execute("DELETE FROM schema_version")
    conn.execute(
        "INSERT INTO schema_version (version, applied_at) VALUES (?, datetime('now'))",
        (version,),
    )
    conn.close()


def test_a_fresh_database_is_initialised_at_the_current_version(tmp_path: Path) -> None:
    conn = db.connect(tmp_path / "new.db")
    assert db.schema_version(conn) == db.SCHEMA_VERSION
    assert db.schema_present(conn)


def test_reading_a_newer_database_never_downgrades_it(tmp_path: Path) -> None:
    """The bug this file exists for.

    Opening used to imply migrating, and every read path opens the database.
    An older build running `list` against a database a newer writer had
    upgraded stamped the version backwards, after which the newer server
    refused to serve a database that was entirely sound.
    """
    path = tmp_path / "newer.db"
    _at_version(path, db.SCHEMA_VERSION + 1)

    with pytest.raises(db.SchemaTooNewError) as exc:
        db.connect(path)
    assert "Upgrade docsearch" in str(exc.value)

    conn = sqlite3.connect(path)
    still = conn.execute("SELECT version FROM schema_version").fetchone()[0]
    conn.close()
    assert still == db.SCHEMA_VERSION + 1, "a failed open must not rewrite the version"


def test_a_newer_database_is_refused_at_both_layers(tmp_path: Path) -> None:
    """connect blocks it, and migrate blocks it independently.

    allow_outdated exists so `docsearch migrate` can open a database behind
    the code. It does not extend to one ahead of it, so migrate is normally
    unreachable for a newer database -- but it guards itself regardless,
    because a guard that depends on every caller taking one route is not one.
    """
    path = tmp_path / "newer.db"
    _at_version(path, db.SCHEMA_VERSION + 3)

    with pytest.raises(db.SchemaTooNewError):
        db.connect(path, allow_outdated=True)

    raw = sqlite3.connect(path)
    raw.row_factory = sqlite3.Row
    with pytest.raises(db.SchemaTooNewError):
        db.migrate(raw)
    still = raw.execute("SELECT version FROM schema_version").fetchone()[0]
    raw.close()
    assert still == db.SCHEMA_VERSION + 3


def test_an_outdated_database_reports_the_remedy_instead_of_migrating(tmp_path: Path) -> None:
    path = tmp_path / "old.db"
    _at_version(path, 1)
    with pytest.raises(db.SchemaOutdatedError) as exc:
        db.connect(path)
    assert "docsearch migrate" in str(exc.value)

    conn = sqlite3.connect(path)
    still = conn.execute("SELECT version FROM schema_version").fetchone()[0]
    conn.close()
    assert still == 1, "opening must not silently upgrade either"


def test_migrate_is_explicit_and_idempotent(tmp_path: Path) -> None:
    path = tmp_path / "old.db"
    _at_version(path, 1)

    conn = db.connect(path, allow_outdated=True)
    result = db.migrate(conn)
    assert result.from_version == 1
    assert result.to_version == db.SCHEMA_VERSION
    assert db.schema_version(conn) == db.SCHEMA_VERSION
    conn.close()

    # Running it again is a no-op, which is what makes it safe as a startup
    # precondition on every worker restart.
    conn = db.connect(path)
    again = db.migrate(conn)
    assert again.columns_added == []
    assert db.schema_version(conn) == db.SCHEMA_VERSION


def test_a_missing_column_is_added_by_migration(tmp_path: Path) -> None:
    path = tmp_path / "partial.db"
    conn = db.connect(path)
    conn.close()

    raw = sqlite3.connect(path)
    raw.execute("ALTER TABLE chunks RENAME TO chunks_old")
    raw.execute(
        "CREATE TABLE chunks (id INTEGER PRIMARY KEY, doc_id TEXT NOT NULL,"
        " ordinal INTEGER NOT NULL, section TEXT, page_start INTEGER, page_end INTEGER,"
        " printed_page_start INTEGER, image_count INTEGER NOT NULL DEFAULT 0,"
        " heading_path TEXT NOT NULL, text TEXT NOT NULL)"
    )
    raw.execute("DROP TABLE chunks_old")
    raw.execute("DELETE FROM schema_version")
    raw.execute("INSERT INTO schema_version (version, applied_at) VALUES (3, datetime('now'))")
    raw.commit()
    raw.close()

    conn = db.connect(path, allow_outdated=True)
    result = db.migrate(conn)
    assert "chunks.kind" in result.columns_added
    cols = {r["name"] for r in conn.execute("PRAGMA table_info(chunks)")}
    assert "kind" in cols


def test_v4_data_survives_the_upgrade_to_v5(tmp_path: Path) -> None:
    """A populated v4 database keeps its rows and gains the new columns.

    The v5 columns are additive and nullable, and source_kind defaults to
    'file' -- every document that predates the site model was one.
    """
    path = tmp_path / "v4.db"
    conn = db.connect(path)
    conn.close()

    raw = sqlite3.connect(path)
    raw.row_factory = sqlite3.Row
    for table, column in (
        ("documents", "source_kind"),
        ("chunks", "url"),
        ("chunks", "fragment"),
    ):
        raw.execute(f"ALTER TABLE {table} DROP COLUMN {column}")
    raw.execute(
        "INSERT INTO documents (doc_id, title, format, source_path, sha256, status,"
        " chunk_count) VALUES ('m','Manual','pdf','/lib/m.pdf','abc','ready',1)"
    )
    raw.execute(
        "INSERT INTO chunks (doc_id, ordinal, heading_path, text)"
        " VALUES ('m', 0, 'Manual > Intro', 'the body text')"
    )
    raw.execute("DELETE FROM schema_version")
    raw.execute("INSERT INTO schema_version (version, applied_at) VALUES (4, datetime('now'))")
    raw.commit()
    raw.close()

    conn = db.connect(path, allow_outdated=True)
    result = db.migrate(conn)

    assert result.from_version == 4
    assert db.schema_version(conn) == 5
    assert set(result.columns_added) == {"documents.source_kind", "chunks.url", "chunks.fragment"}

    doc = conn.execute("SELECT * FROM documents WHERE doc_id='m'").fetchone()
    assert doc["title"] == "Manual"
    assert doc["source_kind"] == "file"

    chunk = conn.execute("SELECT * FROM chunks WHERE doc_id='m'").fetchone()
    assert chunk["text"] == "the body text"
    assert chunk["url"] is None
    assert chunk["fragment"] is None

    hit = conn.execute("SELECT doc_id FROM chunks_fts WHERE chunks_fts MATCH 'body'").fetchone()
    assert hit["doc_id"] == "m"


def test_a_version_is_not_recorded_when_the_schema_does_not_match(tmp_path: Path) -> None:
    """A stamp written without checking makes the claim unfalsifiable.

    The v2 change was semantic: index_terms.section replaced a page column and
    no transformation recovers one from the other. Stamping the current version
    over it would let the readiness gate pass a database whose queries fail.
    """
    path = tmp_path / "ancient.db"
    conn = db.connect(path)
    conn.close()

    raw = sqlite3.connect(path)
    raw.execute("DROP TABLE index_terms")
    raw.execute("CREATE TABLE index_terms (doc_id TEXT NOT NULL, term TEXT NOT NULL, page INTEGER)")
    raw.execute("DELETE FROM schema_version")
    raw.execute("INSERT INTO schema_version (version, applied_at) VALUES (1, datetime('now'))")
    raw.commit()
    raw.close()

    conn = db.connect(path, allow_outdated=True)
    with pytest.raises(db.SchemaMigrationError) as exc:
        db.migrate(conn)
    assert "re-ingest" in str(exc.value)
    assert db.schema_version(conn) == 1, "the version must not advance past a failed migration"
