"""Ingest lifecycle: identity, replacement, visibility, FTS consistency."""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from docsearch.adapters import UnsupportedFormatError
from docsearch.ingest import FileSource, ingest_source, slugify
from docsearch.verify import verify_document


def test_slugify_is_human_readable() -> None:
    assert slugify("grandMA2 Manual") == "grandma2-manual"
    assert slugify("Ünïcode  Títle!") == "unicode-title"


def test_ingest_markdown_produces_ready_document(conn: sqlite3.Connection, md_file: Path) -> None:
    result = ingest_source(conn, FileSource(md_file))
    assert result.outcome == "ingested"
    assert result.chunk_count > 0
    row = conn.execute(
        "SELECT status, chunk_count FROM documents WHERE doc_id = ?", (result.doc_id,)
    ).fetchone()
    assert row["status"] == "ready"
    assert row["chunk_count"] == result.chunk_count


def test_heading_path_records_full_ancestry(conn: sqlite3.Connection, md_file: Path) -> None:
    result = ingest_source(conn, FileSource(md_file))
    paths = [
        r["heading_path"]
        for r in conn.execute("SELECT heading_path FROM chunks WHERE doc_id = ?", (result.doc_id,))
    ]
    assert any(p.startswith("Operator Guide > Executors > Assigning Sequences") for p in paths)


def test_reingest_unchanged_file_is_a_noop(conn: sqlite3.Connection, md_file: Path) -> None:
    first = ingest_source(conn, FileSource(md_file))
    second = ingest_source(conn, FileSource(md_file))
    assert second.outcome == "unchanged"
    assert second.doc_id == first.doc_id
    count = conn.execute("SELECT COUNT(*) c FROM documents").fetchone()["c"]
    assert count == 1


def test_reingest_modified_file_replaces_cleanly(conn: sqlite3.Connection, md_file: Path) -> None:
    first = ingest_source(conn, FileSource(md_file))
    before = conn.execute(
        "SELECT COUNT(*) c FROM chunks WHERE doc_id = ?", (first.doc_id,)
    ).fetchone()["c"]

    md_file.write_text("# Operator Guide\n\n## Only Section\n\nReplaced body text.\n", "utf-8")
    second = ingest_source(conn, FileSource(md_file))

    assert second.outcome == "replaced"
    assert second.doc_id == first.doc_id
    after = conn.execute(
        "SELECT COUNT(*) c FROM chunks WHERE doc_id = ?", (second.doc_id,)
    ).fetchone()["c"]
    assert after != before
    # No orphans anywhere.
    assert conn.execute("SELECT COUNT(*) c FROM documents").fetchone()["c"] == 1
    total_chunks = conn.execute("SELECT COUNT(*) c FROM chunks").fetchone()["c"]
    total_fts = conn.execute("SELECT COUNT(*) c FROM chunks_fts").fetchone()["c"]
    assert total_chunks == after == total_fts


def test_retitled_file_replaces_rather_than_forking_a_second_doc(
    conn: sqlite3.Connection, md_file: Path
) -> None:
    first = ingest_source(conn, FileSource(md_file), title="Original Title")
    second = ingest_source(conn, FileSource(md_file), title="Totally Different Title")
    assert second.doc_id == first.doc_id
    assert conn.execute("SELECT COUNT(*) c FROM documents").fetchone()["c"] == 1


def test_distinct_documents_get_distinct_ids(
    conn: sqlite3.Connection, md_file: Path, txt_file: Path
) -> None:
    a = ingest_source(conn, FileSource(md_file), title="Same Name")
    b = ingest_source(conn, FileSource(txt_file), title="Same Name")
    assert a.doc_id != b.doc_id
    assert b.doc_id.startswith(a.doc_id)


def test_removing_a_document_clears_its_fts_rows(conn: sqlite3.Connection, md_file: Path) -> None:
    from docsearch.db import delete_document_rows

    result = ingest_source(conn, FileSource(md_file))
    assert conn.execute("SELECT COUNT(*) c FROM chunks_fts").fetchone()["c"] > 0
    delete_document_rows(conn, result.doc_id)
    assert conn.execute("SELECT COUNT(*) c FROM chunks_fts").fetchone()["c"] == 0
    hits = conn.execute("SELECT COUNT(*) c FROM chunks_fts WHERE chunks_fts MATCH 'console'")
    assert hits.fetchone()["c"] == 0


def test_unsupported_format_fails_deterministically(
    conn: sqlite3.Connection, tmp_path: Path
) -> None:
    bad = tmp_path / "archive.zip"
    bad.write_bytes(b"PK\x03\x04not really")
    with pytest.raises(UnsupportedFormatError):
        ingest_source(conn, FileSource(bad))


def test_html_and_text_adapters_ingest(
    conn: sqlite3.Connection, html_file: Path, txt_file: Path
) -> None:
    html = ingest_source(conn, FileSource(html_file))
    text = ingest_source(conn, FileSource(txt_file))
    assert html.chunk_count > 0
    assert text.chunk_count > 0
    formats = {r["format"] for r in conn.execute("SELECT format FROM documents")}
    assert formats == {"html", "text"}


def test_verify_reports_no_problems_for_a_clean_ingest(
    conn: sqlite3.Connection, md_file: Path
) -> None:
    result = ingest_source(conn, FileSource(md_file))
    report = verify_document(conn, result.doc_id)
    assert report.problems == []
    assert report.chunk_count == result.chunk_count
