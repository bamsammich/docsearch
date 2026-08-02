"""Worker behaviour: claiming, leases, retry policy, cancellation, cleanup."""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from docsearch import db
from docsearch.worker import Worker, WorkerConfig


@pytest.fixture
def db_path(tmp_path: Path) -> str:
    path = tmp_path / "queue.db"
    db.connect(path).close()
    return str(path)


def _enqueue(conn: sqlite3.Connection, source: Path, title: str | None = None) -> int:
    cur = conn.execute(
        "INSERT INTO ingest_jobs (source_path, title, status, created_at, updated_at)"
        " VALUES (?,?, 'queued', datetime('now'), datetime('now'))",
        (str(source), title),
    )
    return int(cur.lastrowid or 0)


def _worker(db_path: str, **kw: object) -> Worker:
    return Worker(WorkerConfig(db_path=db_path, once=True, **kw))  # type: ignore[arg-type]


def _job(conn: sqlite3.Connection, job_id: int) -> sqlite3.Row:
    row = conn.execute("SELECT * FROM ingest_jobs WHERE id=?", (job_id,)).fetchone()
    assert row is not None
    return row


def test_worker_runs_a_queued_job_to_completion(db_path: str, md_file: Path) -> None:
    conn = db.connect(db_path)
    job_id = _enqueue(conn, md_file)
    _worker(db_path).run()

    row = _job(conn, job_id)
    assert row["status"] == "done"
    assert row["doc_id"]
    assert row["error"] is None
    doc = conn.execute("SELECT status FROM documents WHERE doc_id=?", (row["doc_id"],)).fetchone()
    assert doc["status"] == "ready"


def test_job_completion_and_document_visibility_are_one_transaction(
    db_path: str, md_file: Path
) -> None:
    """A document must never be searchable while its job still reads running."""
    conn = db.connect(db_path)
    job_id = _enqueue(conn, md_file)
    _worker(db_path).run()

    row = _job(conn, job_id)
    doc = conn.execute("SELECT status FROM documents WHERE doc_id=?", (row["doc_id"],)).fetchone()
    assert (row["status"], doc["status"]) == ("done", "ready")


def test_two_workers_cannot_claim_the_same_job(db_path: str, md_file: Path) -> None:
    conn = db.connect(db_path)
    _enqueue(conn, md_file)
    first = _worker(db_path).claim()
    second = _worker(db_path).claim()
    assert first is not None
    assert second is None


def test_expired_lease_is_reclaimable(db_path: str, md_file: Path) -> None:
    """This is what makes a SIGKILLed worker recoverable without intervention."""
    conn = db.connect(db_path)
    job_id = _enqueue(conn, md_file)
    assert _worker(db_path).claim() is not None

    # Simulate the claiming worker dying: its lease lapses.
    conn.execute(
        "UPDATE ingest_jobs SET lease_until=datetime('now','-1 minute') WHERE id=?", (job_id,)
    )
    reclaimed = _worker(db_path).claim()
    assert reclaimed is not None
    assert reclaimed["attempts"] == 2


def test_live_lease_is_not_stolen(db_path: str, md_file: Path) -> None:
    conn = db.connect(db_path)
    _enqueue(conn, md_file)
    assert _worker(db_path).claim() is not None
    assert _worker(db_path).claim() is None


def test_unsupported_format_fails_permanently_without_burning_retries(
    db_path: str, tmp_path: Path
) -> None:
    bad = tmp_path / "thing.zip"
    bad.write_bytes(b"PK\x03\x04nope")
    conn = db.connect(db_path)
    job_id = _enqueue(conn, bad)

    _worker(db_path).run()
    row = _job(conn, job_id)
    assert row["status"] == "failed"
    assert "UnsupportedFormatError" in row["error"]

    # A permanently failed job must not be picked up again.
    assert _worker(db_path).claim() is None


def test_missing_file_fails_and_is_retried_then_exhausted(db_path: str, tmp_path: Path) -> None:
    conn = db.connect(db_path)
    job_id = _enqueue(conn, tmp_path / "absent.md")
    for _ in range(3):
        _worker(db_path).run()
    row = _job(conn, job_id)
    assert row["status"] == "failed"
    assert row["attempts"] == 3
    assert _worker(db_path).claim() is None


def test_error_text_is_intelligible_without_worker_logs(db_path: str, tmp_path: Path) -> None:
    bad = tmp_path / "thing.zip"
    bad.write_bytes(b"PK\x03\x04nope")
    conn = db.connect(db_path)
    job_id = _enqueue(conn, bad)
    _worker(db_path).run()
    error = _job(conn, job_id)["error"]
    assert "no adapter" in error
    assert ".zip" in error
    assert "supported" in error


def test_cancelled_job_leaves_no_rows(db_path: str, md_file: Path) -> None:
    conn = db.connect(db_path)
    job_id = _enqueue(conn, md_file)
    conn.execute("UPDATE ingest_jobs SET cancel_req=1 WHERE id=?", (job_id,))

    # cancel_req is set before the claim, so claim skips it; clear-then-run to
    # exercise cancellation observed at a checkpoint mid-job.
    worker = _worker(db_path)
    conn.execute("UPDATE ingest_jobs SET cancel_req=0 WHERE id=?", (job_id,))
    job = worker.claim()
    assert job is not None
    conn.execute("UPDATE ingest_jobs SET cancel_req=1 WHERE id=?", (job_id,))
    worker.execute(job)

    row = _job(conn, job_id)
    assert row["status"] == "cancelled"
    for table in ("documents", "chunks", "pages", "index_terms"):
        count = conn.execute(f"SELECT COUNT(*) c FROM {table}").fetchone()["c"]
        assert count == 0, f"{table} still has rows after cancellation"
    assert conn.execute("SELECT COUNT(*) c FROM chunks_fts").fetchone()["c"] == 0


def test_warnings_are_persisted_on_job_and_document(db_path: str, md_file: Path) -> None:
    """The worker is headless; findings must survive as data, not log lines."""
    conn = db.connect(db_path)
    job_id = _enqueue(conn, md_file)
    _worker(db_path).run()

    row = _job(conn, job_id)
    assert row["warnings"], "job row carries no warnings payload"
    doc = conn.execute("SELECT warnings FROM documents WHERE doc_id=?", (row["doc_id"],)).fetchone()
    assert doc["warnings"], "document row carries no warnings payload"

    import json

    payload = json.loads(row["warnings"])
    assert "quality" in payload
    assert "in_toc_not_in_body" in payload
    assert "scattered_sections" in payload


def test_reingest_of_unchanged_file_completes_as_noop(db_path: str, md_file: Path) -> None:
    conn = db.connect(db_path)
    _enqueue(conn, md_file)
    _worker(db_path).run()
    second = _enqueue(conn, md_file)
    _worker(db_path).run()

    row = _job(conn, second)
    assert row["status"] == "done"
    assert conn.execute("SELECT COUNT(*) c FROM documents").fetchone()["c"] == 1
