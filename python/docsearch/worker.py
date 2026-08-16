"""The ingest worker.

A long-running process that polls ``ingest_jobs`` and executes them one at a
time. It calls the same :func:`~docsearch.ingest.ingest_file` the CLI does --
the only difference is who drives it and who observes progress.

Crash recovery rests on the lease. A claimed job carries ``lease_until``, and
the claim query will take back any running job whose lease has expired, so a
worker killed mid-job is recovered by the next one without intervention.
"""

from __future__ import annotations

import logging
import os
import signal
import sqlite3
import time
from dataclasses import dataclass
from pathlib import Path
from types import FrameType

from . import db
from .errors import IngestCancelled, PermanentIngestError
from .ingest import IngestResult, ingest_source, source_for

log = logging.getLogger("docsearch.worker")

DEFAULT_LEASE_SECONDS = 300
DEFAULT_POLL_SECONDS = 2.0
DEFAULT_MAX_ATTEMPTS = 3

#: Minimum seconds between progress writes. Progress must be frequent enough
#: that a long OCR pass is visibly advancing -- a status tool reporting only
#: "running" for forty minutes is indistinguishable from a hang -- but each
#: write also renews the lease, so it need not be more often than this.
PROGRESS_INTERVAL_SECONDS = 2.0

_CLAIM = """
UPDATE ingest_jobs
   SET status='running',
       attempts=attempts+1,
       phase=NULL,
       progress_cur=NULL,
       progress_tot=NULL,
       error=NULL,
       lease_until=datetime('now', ?),
       updated_at=datetime('now')
 WHERE id = (
       SELECT id FROM ingest_jobs
        WHERE cancel_req = 0
          AND attempts < ?
          AND (status='queued'
               OR (status='running' AND lease_until < datetime('now')))
        ORDER BY created_at, id
        LIMIT 1)
RETURNING *
"""


@dataclass(slots=True)
class WorkerConfig:
    db_path: str
    root: Path | None = None
    #: Raw HTTP responses, kept apart from the search index. Defaults beside
    #: the database, which is where the operator already grants write access.
    cache_path: Path | None = None
    lease_seconds: int = DEFAULT_LEASE_SECONDS
    poll_seconds: float = DEFAULT_POLL_SECONDS
    max_attempts: int = DEFAULT_MAX_ATTEMPTS
    once: bool = False


class Worker:
    def __init__(self, config: WorkerConfig) -> None:
        self.config = config
        self.conn = db.connect(config.db_path)
        self.cache_path = config.cache_path or Path(config.db_path).with_name("fetch-cache.db")
        self._stopping = False

    # -- lifecycle ---------------------------------------------------------
    def install_signal_handlers(self) -> None:
        def handle(signum: int, _frame: FrameType | None) -> None:
            log.info("received signal %s; finishing current job then exiting", signum)
            self._stopping = True

        signal.signal(signal.SIGTERM, handle)
        signal.signal(signal.SIGINT, handle)

    def run(self) -> None:
        log.info("worker started (db=%s root=%s)", self.config.db_path, self.config.root)
        while not self._stopping:
            job = self.claim()
            if job is None:
                if self.config.once:
                    return
                time.sleep(self.config.poll_seconds)
                continue
            self.execute(job)
            if self.config.once:
                return

    # -- claiming ----------------------------------------------------------
    def claim(self) -> sqlite3.Row | None:
        """Claim one job atomically.

        A single statement, so two workers racing cannot take the same row.
        Jobs at or past the attempt ceiling are excluded here rather than
        after claiming, otherwise a permanently failing job is reclaimed
        forever and starves the queue.
        """
        self.conn.execute("BEGIN IMMEDIATE")
        try:
            row: sqlite3.Row | None = self.conn.execute(
                _CLAIM, (f"+{self.config.lease_seconds} seconds", self.config.max_attempts)
            ).fetchone()
            self.conn.execute("COMMIT")
        except Exception:
            self.conn.execute("ROLLBACK")
            raise
        if row is not None:
            log.info(
                "claimed job %s (attempt %s): %s",
                row["id"],
                row["attempts"],
                row["source_path"],
            )
        return row

    # -- execution ---------------------------------------------------------
    def execute(self, job: sqlite3.Row) -> None:
        job_id = int(job["id"])
        last_write = 0.0

        def progress(phase: str, cur: int, tot: int) -> None:
            nonlocal last_write
            now = time.monotonic()
            if now - last_write < PROGRESS_INTERVAL_SECONDS and cur < tot:
                return
            last_write = now
            # Every progress write renews the lease: a job that is visibly
            # advancing must never be reclaimed out from under this worker.
            self.conn.execute(
                "UPDATE ingest_jobs SET phase=?, progress_cur=?, progress_tot=?,"
                " lease_until=datetime('now', ?), updated_at=datetime('now') WHERE id=?",
                (phase, cur, tot, f"+{self.config.lease_seconds} seconds", job_id),
            )

        def should_cancel() -> bool:
            row = self.conn.execute(
                "SELECT cancel_req FROM ingest_jobs WHERE id=?", (job_id,)
            ).fetchone()
            return bool(row and row["cancel_req"])

        def record_doc_id(doc_id: str) -> None:
            self.conn.execute(
                "UPDATE ingest_jobs SET doc_id=?, updated_at=datetime('now') WHERE id=?",
                (doc_id, job_id),
            )

        def finalize(conn: sqlite3.Connection, result: IngestResult) -> None:
            conn.execute(
                "UPDATE ingest_jobs SET status='done', phase=NULL, doc_id=?, error=NULL,"
                " warnings=?, lease_until=NULL, updated_at=datetime('now') WHERE id=?",
                (result.doc_id, result.report.to_json() if result.report else None, job_id),
            )

        try:
            result = ingest_source(
                self.conn,
                source_for(job["source_path"], cache_path=self.cache_path),
                title=job["title"],
                progress=progress,
                should_cancel=should_cancel,
                on_doc_id=record_doc_id,
                on_finalize=finalize,
            )
        except IngestCancelled:
            self._cancelled(job_id)
            return
        except PermanentIngestError as exc:
            self._failed(job_id, exc, permanent=True)
            return
        except Exception as exc:
            self._failed(job_id, exc, permanent=False)
            return

        if result.outcome == "unchanged":
            # The final transaction never ran: nothing was re-indexed.
            self.conn.execute(
                "UPDATE ingest_jobs SET status='done', phase=NULL, doc_id=?, error=NULL,"
                " lease_until=NULL, updated_at=datetime('now') WHERE id=?",
                (result.doc_id, job_id),
            )
        log.info(
            "job %s %s -> %s (%s chunks, quality=%s)",
            job_id,
            result.outcome,
            result.doc_id,
            result.chunk_count,
            result.report.quality() if result.report else "unknown",
        )

    # -- terminal states ---------------------------------------------------
    def _cleanup_partial(self, job_id: int) -> None:
        row = self.conn.execute("SELECT doc_id FROM ingest_jobs WHERE id=?", (job_id,)).fetchone()
        doc_id = row["doc_id"] if row else None
        if not doc_id:
            return
        state = self.conn.execute(
            "SELECT status FROM documents WHERE doc_id=?", (doc_id,)
        ).fetchone()
        # Only ever remove a document this job left mid-flight. A 'ready' row
        # under this doc_id is a previously good ingest and must survive.
        if state is not None and state["status"] != "ready":
            db.delete_document_rows(self.conn, doc_id)

    def _cancelled(self, job_id: int) -> None:
        self.conn.execute("BEGIN IMMEDIATE")
        try:
            self._cleanup_partial(job_id)
            self.conn.execute(
                "UPDATE ingest_jobs SET status='cancelled', phase=NULL, lease_until=NULL,"
                " error='cancelled at operator request', updated_at=datetime('now')"
                " WHERE id=?",
                (job_id,),
            )
            self.conn.execute("COMMIT")
        except Exception:
            self.conn.execute("ROLLBACK")
            raise
        log.info("job %s cancelled; partial rows removed", job_id)

    def _failed(self, job_id: int, exc: BaseException, *, permanent: bool) -> None:
        message = f"{type(exc).__name__}: {exc}"
        self.conn.execute("BEGIN IMMEDIATE")
        try:
            self._cleanup_partial(job_id)
            row = self.conn.execute(
                "SELECT attempts FROM ingest_jobs WHERE id=?", (job_id,)
            ).fetchone()
            attempts = int(row["attempts"]) if row else self.config.max_attempts
            exhausted = permanent or attempts >= self.config.max_attempts
            # attempts is left at its true value. A status of 'failed' is
            # already unclaimable -- the claim query only ever considers
            # 'queued' and lease-expired 'running' -- so inflating the counter
            # to block reclaim would buy nothing and would make a job that
            # failed deterministically on attempt one indistinguishable from
            # one that genuinely exhausted three. The distinction is carried by
            # the permanent flag instead, and reported as such.
            self.conn.execute(
                "UPDATE ingest_jobs SET status=?, phase=NULL, error=?, permanent=?,"
                " lease_until=NULL, updated_at=datetime('now') WHERE id=?",
                (
                    "failed" if exhausted else "queued",
                    message,
                    1 if permanent else 0,
                    job_id,
                ),
            )
            self.conn.execute("COMMIT")
        except Exception:
            self.conn.execute("ROLLBACK")
            raise
        log.warning(
            "job %s failed (%s, attempt %s): %s",
            job_id,
            "permanent" if permanent else "transient",
            attempts,
            message,
        )


def run_worker(config: WorkerConfig) -> None:
    logging.basicConfig(
        level=os.environ.get("DOCSEARCH_LOG_LEVEL", "INFO"),
        format="%(asctime)s %(levelname)-7s %(name)s %(message)s",
    )
    worker = Worker(config)
    worker.install_signal_handlers()
    worker.run()
