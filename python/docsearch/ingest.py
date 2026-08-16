"""The one ingest path.

``docsearch ingest``, ``docsearch add`` and the worker all call
:func:`ingest_source`. The only difference is who drives it and who reports
progress -- the logic is not forked.

A file and a site are the same ingest with different answers to four
questions, so they are two :class:`Source` implementations rather than two
pipelines. One transaction boundary, one progress model and one set of quality
gates serve both:

============  ==============================  ==============================
              file                            site
============  ==============================  ==============================
guard         inside a configured root        urlguard: scheme, host, address
acquire       read from disk                  fetch, or a cache hit
identity      absolute path                   canonical base URL
extract       adapter by suffix               walk the nav, parse each page
============  ==============================  ==============================
"""

from __future__ import annotations

import contextlib
import hashlib
import re
import sqlite3
import unicodedata
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path
from typing import Protocol
from urllib.parse import urlsplit

from . import db, structure
from .adapters import for_path
from .blocks import Extraction
from .chunker import chunk as chunk_extraction
from .errors import IngestCancelled, StructureValidationError
from .structure import StructureReport
from .tokens import uncalibrated_letter_share

#: ``(phase, current, total)``; phase is one of 'discover', 'fetch', 'extract',
#: 'chunk', 'index'.
ProgressFn = Callable[[str, int, int], None]
CancelFn = Callable[[], bool]
#: Runs inside the final transaction, alongside the document becoming visible.
FinalizeFn = Callable[[sqlite3.Connection, "IngestResult"], None]

CHUNK_INSERT_BATCH = 200

__all__ = [
    "FileSource",
    "IngestCancelled",
    "IngestResult",
    "SiteSource",
    "Source",
    "ingest_source",
    "is_url",
    "slugify",
    "source_for",
    "unique_doc_id",
]


def is_url(target: str | Path) -> bool:
    """Whether a job target names a site rather than a path on disk."""
    return urlsplit(str(target)).scheme in ("http", "https")


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


class Source(Protocol):
    """Something that can become one document."""

    #: 'file' | 'site'. Stored on the document so a reader knows what it is.
    kind: str

    def identity(self) -> str:
        """What replacement is keyed on: a path, or a canonical base URL."""

    def acquire(self, *, progress: ProgressFn | None, checkpoint: Callable[[], None]) -> None:
        """Guard, then obtain the bytes. Raises rather than returning empty."""

    def digest(self) -> str:
        """Content hash, for deciding a re-ingest is a no-op."""

    def extract(self, *, progress: ProgressFn | None) -> Extraction:
        """The normalized intermediate, after :meth:`acquire`."""


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


# -- sources ---------------------------------------------------------------


@dataclass(slots=True)
class FileSource:
    """One file on disk, extracted by the adapter its suffix selects."""

    path: Path
    kind: str = field(default="file", init=False)
    _digest: str = field(default="", init=False)

    def identity(self) -> str:
        return str(self.path)

    def acquire(self, *, progress: ProgressFn | None, checkpoint: Callable[[], None]) -> None:
        self.path = self.path.resolve()
        if not self.path.is_file():
            raise FileNotFoundError(f"not a file: {self.path}")
        # Raises UnsupportedFormatError, which is deterministic and never
        # retried. Done here so an unusable file fails before it is hashed.
        for_path(self.path)
        checkpoint()
        self._digest = sha256_file(self.path)

    def digest(self) -> str:
        return self._digest

    def extract(self, *, progress: ProgressFn | None) -> Extraction:
        return for_path(self.path)(self.path, progress)


@dataclass(slots=True)
class SiteSource:
    """A documentation site, crawled and treated as one document."""

    seed: str
    cache_path: Path
    max_pages: int = 500
    link_depth: int = 3
    #: False re-chunks an already-crawled site without a single request.
    revalidate: bool = True
    obey_robots: bool = True
    #: Seconds between requests to one host. None takes the fetcher's default,
    #: which is what a stranger's site gets; an operator crawling their own may
    #: reasonably lower it.
    interval: float | None = None
    #: Injectable exactly as the fetcher's own guards are, and for the same
    #: reason: the real guard refuses loopback, which is precisely what a test
    #: server is. That the default blocks it is covered by urlguard's tests in
    #: both languages, against a shared table of address verdicts.
    guard: Callable[[str], object] | None = None
    addr_guard: Callable[[str], bool] | None = None
    kind: str = field(default="site", init=False)
    _result: object = field(default=None, init=False)

    def identity(self) -> str:
        from .fetch import normalize

        return normalize(self.seed)

    def acquire(self, *, progress: ProgressFn | None, checkpoint: Callable[[], None]) -> None:
        from . import fetchcache
        from .crawl import crawl
        from .fetch import DEFAULT_INTERVAL, Fetcher
        from .urlguard import addr_allowed, check

        guard = self.guard or check
        addr_guard = self.addr_guard or addr_allowed
        # Validated here as well as inside the fetcher. A job row is not proof
        # that anything validated it, and this is the worker.
        guard(self.seed)

        cache = fetchcache.connect(self.cache_path)
        with Fetcher(
            cache,
            guard=guard,
            addr_guard=addr_guard,
            obey_robots=self.obey_robots,
            interval=DEFAULT_INTERVAL if self.interval is None else self.interval,
        ) as fetcher:
            self._result = crawl(
                fetcher,
                self.seed,
                progress=progress,
                should_cancel=lambda: _cancelled(checkpoint),
                max_pages=self.max_pages,
                link_depth=self.link_depth,
                revalidate=self.revalidate,
            )

    def digest(self) -> str:
        from .crawl import CrawlResult

        result = self._result
        assert isinstance(result, CrawlResult)
        h = hashlib.sha256()
        for url in sorted(result.pages):
            h.update(url.encode())
            h.update(hashlib.sha256(result.pages[url].body).digest())
        return h.hexdigest()

    def extract(self, *, progress: ProgressFn | None) -> Extraction:
        from .crawl import CrawlResult
        from .site import build_extraction

        result = self._result
        assert isinstance(result, CrawlResult)
        if not result.pages:
            raise StructureValidationError(
                f"{self.seed}: no page of this site could be fetched. " + "; ".join(result.notes)
            )
        return build_extraction(result)


def _cancelled(checkpoint: Callable[[], None]) -> bool:
    """Adapt a raising checkpoint to the predicate the crawler expects."""
    checkpoint()
    return False


def source_for(
    target: str | Path,
    *,
    cache_path: Path | None = None,
    revalidate: bool = True,
) -> Source:
    """Pick the source a target names.

    The worker gets a job row holding either an absolute path or a URL, and
    this is where that string becomes one kind of ingest or the other.
    """
    if is_url(target):
        if cache_path is None:
            raise ValueError("a site source needs a fetch-cache path")
        return SiteSource(seed=str(target), cache_path=cache_path, revalidate=revalidate)
    return FileSource(path=Path(target))


# -- the pipeline ----------------------------------------------------------


def ingest_source(
    conn: sqlite3.Connection,
    source: Source,
    *,
    title: str | None = None,
    progress: ProgressFn | None = None,
    should_cancel: CancelFn | None = None,
    on_doc_id: Callable[[str], None] | None = None,
    on_finalize: FinalizeFn | None = None,
) -> IngestResult:
    """Acquire, extract, chunk and index one source.

    The document is invisible to search until the final transaction flips
    ``documents.status`` to ``'ready'``.
    """

    def checkpoint() -> None:
        if should_cancel and should_cancel():
            raise IngestCancelled()

    checkpoint()
    source.acquire(progress=progress, checkpoint=checkpoint)
    digest = source.digest()
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

    # Replacement is keyed on identity, not on the title slug: a retitled
    # source must replace its own rows rather than slugify into a second doc_id
    # and orphan the originals.
    identity = source.identity()
    prior = conn.execute(
        "SELECT doc_id FROM documents WHERE source_path = ?", (identity,)
    ).fetchone()
    replacing = prior["doc_id"] if prior else None

    extraction = source.extract(progress=progress)
    checkpoint()

    report = structure.from_diagnostics(extraction.diagnostics)
    if report.fatal:
        # Refused before any row is written, so there is nothing to roll back.
        raise StructureValidationError(report.failure_message())

    doc_title = (title or extraction.title or identity).strip()
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
            f"{identity}: extraction produced no chunks. "
            f"Structure source was '{report.structure_source}' and "
            f"{report.body_sections} section heading(s) were found, but no body text "
            "survived. The document was not indexed."
        )
    report.scattered_sections = structure.scattered_sections(chunks)
    # Assessed on the chunks rather than on the derived section list: a source
    # can declare plenty of sections and still leave consecutive chunks sharing
    # one heading, and it is the chunks a caller actually filters and reads.
    report.chunks = len(chunks)
    report.distinct_heading_paths = len({c.heading_path for c in chunks})
    report.headless_chunks = sum(1 for c in chunks if not c.heading_path.strip())
    # Sampled evenly rather than from the front: front matter is often in a
    # different script from the body, and a title page proves nothing about
    # the manual behind it.
    sample = chunks[:: max(1, len(chunks) // 200)]
    report.uncalibrated_script_share = round(
        uncalibrated_letter_share("\n".join(c.text for c in sample)), 3
    )
    checkpoint()

    conn.execute("BEGIN IMMEDIATE")
    try:
        if replacing:
            db.delete_document_rows(conn, replacing)
        conn.execute(
            "INSERT INTO documents (doc_id, title, format, source_path, source_kind, sha256,"
            " page_count, chunk_count, status, ingested_at)"
            " VALUES (?,?,?,?,?,?,?,NULL,'ingesting',NULL)",
            (
                doc_id,
                doc_title,
                extraction.format,
                identity,
                source.kind,
                digest,
                extraction.page_count,
            ),
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
                " printed_page_start, image_count, kind, url, fragment, heading_path, text)"
                " VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
                [
                    (
                        doc_id,
                        c.ordinal,
                        c.section,
                        c.page_start,
                        c.page_end,
                        c.printed_page_start,
                        c.image_count,
                        c.kind,
                        c.url,
                        c.fragment,
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
