"""The one ingest path.

``docsearch ingest`` and the worker both call :func:`ingest_file`. The only
difference is who drives it and who reports progress -- the logic is not forked.
"""

from __future__ import annotations

import contextlib
import hashlib
import re
import sqlite3
import unicodedata
from collections.abc import Callable
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

from . import db, structure
from .adapters import for_path
from .chunker import chunk as chunk_extraction
from .errors import IngestCancelled, StructureValidationError
from .structure import StructureReport

#: ``(phase, current, total)``; phase is 'extract' | 'chunk' | 'index'.
ProgressFn = Callable[[str, int, int], None]
CancelFn = Callable[[], bool]
#: Runs inside the final transaction, alongside the document becoming visible.
FinalizeFn = Callable[[sqlite3.Connection, "IngestResult"], None]

CHUNK_INSERT_BATCH = 200

__all__ = ["IngestCancelled", "IngestResult", "ingest_file", "slugify", "unique_doc_id"]


@dataclass(slots=True)
class IngestResult:
    doc_id: str
    title: str
    chunk_count: int
    #: 'ingested' | 'replaced' | 'unchanged'
    outcome: str
    note: str = ""
    diagnostics: dict[str, object] | None = None
    report: StructureReport | None = None


def slugify(value: str) -> str:
    norm = unicodedata.normalize("NFKD", value).encode("ascii", "ignore").decode()
    slug = re.sub(r"[^a-z0-9]+", "-", norm.lower()).strip("-")
    return slug or "document"


def unique_doc_id(conn: sqlite3.Connection, title: str, *, reuse: str | None = None) -> str:
    base = slugify(title)
    if reuse is not None:
        return reuse
    taken = {
        r["doc_id"]
        for r in conn.execute("SELECT doc_id FROM documents WHERE doc_id LIKE ?", (base + "%",))
    }
    if base not in taken:
        return base
    n = 2
    while f"{base}-{n}" in taken:
        n += 1
    return f"{base}-{n}"


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for block in iter(lambda: fh.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def _now() -> str:
    return datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def ingest_file(
    conn: sqlite3.Connection,
    path: Path,
    *,
    title: str | None = None,
    progress: ProgressFn | None = None,
    should_cancel: CancelFn | None = None,
    on_doc_id: Callable[[str], None] | None = None,
    on_finalize: FinalizeFn | None = None,
) -> IngestResult:
    """Extract, chunk and index one file.

    The document is invisible to search until the final transaction flips
    ``documents.status`` to ``'ready'``.
    """
    path = path.resolve()
    if not path.is_file():
        raise FileNotFoundError(f"not a file: {path}")

    adapter = for_path(path)  # raises UnsupportedFormatError; deterministic
    digest = sha256_file(path)

    def checkpoint() -> None:
        if should_cancel and should_cancel():
            raise IngestCancelled()

    checkpoint()

    same = conn.execute(
        "SELECT doc_id, title FROM documents WHERE sha256 = ? AND status = 'ready'",
        (digest,),
    ).fetchone()
    if same is not None:
        return IngestResult(
            doc_id=same["doc_id"],
            title=same["title"],
            chunk_count=conn.execute(
                "SELECT COUNT(*) c FROM chunks WHERE doc_id = ?", (same["doc_id"],)
            ).fetchone()["c"],
            outcome="unchanged",
            note=f"content hash already ingested as '{same['doc_id']}'; nothing to do",
        )

    # Replacement is keyed on source path, not on the title slug: a retitled
    # file must replace its own rows rather than slugify into a second doc_id
    # and orphan the originals.
    prior = conn.execute(
        "SELECT doc_id FROM documents WHERE source_path = ?", (str(path),)
    ).fetchone()
    replacing = prior["doc_id"] if prior else None

    extraction = adapter(path, progress)
    checkpoint()

    report = structure.from_diagnostics(extraction.diagnostics)
    if report.fatal:
        # Refused before any row is written, so there is nothing to roll back.
        raise StructureValidationError(report.failure_message())

    doc_title = (title or extraction.title or path.stem).strip()
    doc_id = unique_doc_id(conn, doc_title, reuse=replacing)
    if on_doc_id:
        on_doc_id(doc_id)

    if progress:
        progress("chunk", 0, 1)
    chunks = chunk_extraction(extraction)
    if not chunks:
        # A document that indexes to nothing is not an empty success. It holds
        # a doc_id, reports 'ready', and can never be returned by any query --
        # extraction found structure but no text survived, which is a defect
        # worth surfacing rather than recording as a valid ingest.
        raise StructureValidationError(
            f"{path.name}: extraction produced no chunks. "
            f"Structure source was '{report.structure_source}' and "
            f"{report.body_sections} section heading(s) were found, but no body text "
            "survived boilerplate stripping. The document was not indexed."
        )
    report.scattered_sections = structure.scattered_sections(chunks)
    checkpoint()

    conn.execute("BEGIN IMMEDIATE")
    try:
        if replacing:
            db.delete_document_rows(conn, replacing)
        conn.execute(
            "INSERT INTO documents (doc_id, title, format, source_path, sha256, page_count,"
            " chunk_count, status, ingested_at) VALUES (?,?,?,?,?,?,NULL,'ingesting',NULL)",
            (doc_id, doc_title, extraction.format, str(path), digest, extraction.page_count),
        )
        conn.execute("COMMIT")
    except Exception:
        conn.execute("ROLLBACK")
        raise

    try:
        total = len(chunks)
        for start in range(0, total, CHUNK_INSERT_BATCH):
            checkpoint()
            batch = chunks[start : start + CHUNK_INSERT_BATCH]
            conn.execute("BEGIN IMMEDIATE")
            conn.executemany(
                "INSERT INTO chunks (doc_id, ordinal, section, page_start, page_end,"
                " printed_page_start, image_count, heading_path, text)"
                " VALUES (?,?,?,?,?,?,?,?,?)",
                [
                    (
                        doc_id,
                        c.ordinal,
                        c.section,
                        c.page_start,
                        c.page_end,
                        c.printed_page_start,
                        c.image_count,
                        c.heading_path,
                        c.text,
                    )
                    for c in batch
                ],
            )
            conn.execute("COMMIT")
            if progress:
                progress("index", min(start + CHUNK_INSERT_BATCH, total), total)

        checkpoint()
        conn.execute("BEGIN IMMEDIATE")
        if extraction.pages:
            conn.executemany(
                "INSERT OR REPLACE INTO pages (doc_id, page, text) VALUES (?,?,?)",
                [(doc_id, p, t) for p, t in extraction.pages.items()],
            )
        if extraction.index_terms:
            conn.executemany(
                "INSERT INTO index_terms (doc_id, term, section) VALUES (?,?,?)",
                [(doc_id, term, sec) for term, sec in extraction.index_terms],
            )
        conn.execute("COMMIT")

        result = IngestResult(
            doc_id=doc_id,
            title=doc_title,
            chunk_count=len(chunks),
            outcome="replaced" if replacing else "ingested",
            diagnostics=extraction.diagnostics,
            report=report,
        )

        # The moment the document becomes visible to search. The job row is
        # completed in this same transaction so a document can never be
        # searchable while its job still reads as running.
        conn.execute("BEGIN IMMEDIATE")
        conn.execute(
            "UPDATE documents SET status='ready', chunk_count=?, ingested_at=?, warnings=?"
            " WHERE doc_id=?",
            (len(chunks), _now(), report.to_json(), doc_id),
        )
        if on_finalize:
            on_finalize(conn, result)
        conn.execute("COMMIT")
    except Exception:
        with contextlib.suppress(sqlite3.OperationalError):
            conn.execute("ROLLBACK")
        conn.execute("BEGIN IMMEDIATE")
        db.delete_document_rows(conn, doc_id)
        conn.execute("COMMIT")
        raise

    return result
