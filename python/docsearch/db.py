"""SQLite storage: schema, connections, pragmas.

One SQLite file is the entire storage layer. WAL is mandatory, not optional --
the worker writes while the server reads, and rollback-journal mode deadlocks
them against each other.
"""

from __future__ import annotations

import sqlite3
from pathlib import Path

BUSY_TIMEOUT_MS = 5000

#: Tables ``/readyz`` and the CLI check for to decide the schema is present.
REQUIRED_TABLES = (
    "documents",
    "ingest_jobs",
    "chunks",
    "chunks_fts",
    "pages",
    "index_terms",
)

SCHEMA = """
CREATE TABLE IF NOT EXISTS documents (
  doc_id       TEXT PRIMARY KEY,
  title        TEXT NOT NULL,
  format       TEXT NOT NULL,
  source_path  TEXT NOT NULL,
  sha256       TEXT NOT NULL,
  page_count   INTEGER,
  chunk_count  INTEGER,
  status       TEXT NOT NULL,
  ingested_at  TEXT
);
CREATE INDEX IF NOT EXISTS idx_documents_source ON documents(source_path);
CREATE INDEX IF NOT EXISTS idx_documents_sha    ON documents(sha256);

CREATE TABLE IF NOT EXISTS ingest_jobs (
  id            INTEGER PRIMARY KEY,
  source_path   TEXT NOT NULL,
  title         TEXT,
  doc_id        TEXT,
  status        TEXT NOT NULL,
  phase         TEXT,
  progress_cur  INTEGER,
  progress_tot  INTEGER,
  attempts      INTEGER NOT NULL DEFAULT 0,
  cancel_req    INTEGER NOT NULL DEFAULT 0,
  error         TEXT,
  lease_until   TEXT,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON ingest_jobs(status, created_at);

CREATE TABLE IF NOT EXISTS chunks (
  id                 INTEGER PRIMARY KEY,
  doc_id             TEXT NOT NULL REFERENCES documents(doc_id),
  ordinal            INTEGER NOT NULL,
  section            TEXT,
  page_start         INTEGER,
  page_end           INTEGER,
  printed_page_start INTEGER,
  image_count        INTEGER NOT NULL DEFAULT 0,
  heading_path       TEXT NOT NULL,
  text               TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chunks_doc_ord     ON chunks(doc_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_chunks_doc_section ON chunks(doc_id, section);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
  text,
  heading_path,
  doc_id UNINDEXED,
  content='chunks',
  content_rowid='id'
);

-- An external-content FTS5 table does not maintain itself. Without these
-- triggers a DELETE on chunks leaves orphaned FTS rows that still match, so
-- a removed or re-ingested document keeps answering queries.
CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
  INSERT INTO chunks_fts(rowid, text, heading_path, doc_id)
  VALUES (new.id, new.text, new.heading_path, new.doc_id);
END;
CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
  INSERT INTO chunks_fts(chunks_fts, rowid, text, heading_path, doc_id)
  VALUES ('delete', old.id, old.text, old.heading_path, old.doc_id);
END;
CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
  INSERT INTO chunks_fts(chunks_fts, rowid, text, heading_path, doc_id)
  VALUES ('delete', old.id, old.text, old.heading_path, old.doc_id);
  INSERT INTO chunks_fts(rowid, text, heading_path, doc_id)
  VALUES (new.id, new.text, new.heading_path, new.doc_id);
END;

CREATE TABLE IF NOT EXISTS pages (
  doc_id TEXT NOT NULL REFERENCES documents(doc_id),
  page   INTEGER NOT NULL,
  text   TEXT NOT NULL,
  PRIMARY KEY (doc_id, page)
);

CREATE TABLE IF NOT EXISTS index_terms (
  doc_id  TEXT NOT NULL REFERENCES documents(doc_id),
  term    TEXT NOT NULL,
  section TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_index_terms ON index_terms(doc_id, term);
"""


def connect(db_path: str | Path, *, create: bool = True) -> sqlite3.Connection:
    """Open ``db_path`` with the pragmas both processes must agree on."""
    path = Path(db_path)
    if create:
        path.parent.mkdir(parents=True, exist_ok=True)
    elif not path.exists():
        raise FileNotFoundError(f"database does not exist: {path}")

    conn = sqlite3.connect(path, isolation_level=None, timeout=BUSY_TIMEOUT_MS / 1000)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute(f"PRAGMA busy_timeout={BUSY_TIMEOUT_MS}")
    conn.execute("PRAGMA foreign_keys=ON")
    conn.execute("PRAGMA synchronous=NORMAL")
    if create:
        conn.executescript(SCHEMA)
    return conn


def schema_present(conn: sqlite3.Connection) -> bool:
    """True when every required table exists."""
    rows = conn.execute("SELECT name FROM sqlite_master WHERE type IN ('table','view')").fetchall()
    have = {r["name"] for r in rows}
    return all(t in have for t in REQUIRED_TABLES)


def delete_document_rows(conn: sqlite3.Connection, doc_id: str) -> None:
    """Remove every trace of ``doc_id``.

    Ordering matters: chunks last is wrong -- the AFTER DELETE trigger on
    chunks is what clears chunks_fts, so chunks must be deleted through SQL
    (never via a bulk table drop) for the FTS index to stay consistent.
    """
    conn.execute("DELETE FROM chunks WHERE doc_id = ?", (doc_id,))
    conn.execute("DELETE FROM pages WHERE doc_id = ?", (doc_id,))
    conn.execute("DELETE FROM index_terms WHERE doc_id = ?", (doc_id,))
    conn.execute("DELETE FROM documents WHERE doc_id = ?", (doc_id,))
