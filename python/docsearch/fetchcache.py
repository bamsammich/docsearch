"""Raw HTTP responses, kept apart from the search index.

A separate database on purpose. The search index is read by the Go server on
every request and is the artifact shipped for offline use; this is worker-only,
disposable, and holds raw bytes that would otherwise bloat it permanently.

Keeping it buys four things:

* Crawl and ingest become separable. A chunker change re-indexes a whole site
  with no requests at all.
* Conditional ``GET`` on refresh, so a re-crawl transfers only what changed.
* A cancelled or crashed crawl resumes rather than restarting.
* When extraction goes wrong, what was actually fetched is inspectable.

Because it is disposable there is no migration path: a file at the wrong
version is rebuilt. Nothing here is a source of truth, so recreating it costs
bandwidth and nothing else.
"""

from __future__ import annotations

import sqlite3
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

#: Bumped when the shape changes. A mismatch rebuilds the file.
CACHE_VERSION = 1

SCHEMA = """
CREATE TABLE IF NOT EXISTS responses (
  url            TEXT PRIMARY KEY,   -- normalized request URL
  final_url      TEXT NOT NULL,      -- after redirects; equals url when none
  status         INTEGER NOT NULL,
  content_type   TEXT,
  etag           TEXT,
  last_modified  TEXT,
  body           BLOB NOT NULL,
  sha256         TEXT NOT NULL,
  fetched_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS robots (
  host        TEXT PRIMARY KEY,
  body        TEXT NOT NULL,        -- empty when the host serves no robots.txt
  fetched_at  TEXT NOT NULL
);
"""


@dataclass(slots=True, frozen=True)
class CachedResponse:
    url: str
    final_url: str
    status: int
    content_type: str | None
    etag: str | None
    last_modified: str | None
    body: bytes
    sha256: str
    fetched_at: str


def _now() -> str:
    return datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def connect(path: str | Path) -> sqlite3.Connection:
    """Open the cache, rebuilding it if it was written by another version."""
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(p, isolation_level=None)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA synchronous=NORMAL")

    found = int(conn.execute("PRAGMA user_version").fetchone()[0])
    if found and found != CACHE_VERSION:
        # Disposable: a stale cache is bandwidth, not data. Dropping beats
        # carrying migration machinery for something regenerable.
        conn.executescript("DROP TABLE IF EXISTS responses; DROP TABLE IF EXISTS robots;")
        found = 0
    conn.executescript(SCHEMA)
    if found != CACHE_VERSION:
        conn.execute(f"PRAGMA user_version={CACHE_VERSION}")
    return conn


def get(conn: sqlite3.Connection, url: str) -> CachedResponse | None:
    row = conn.execute("SELECT * FROM responses WHERE url = ?", (url,)).fetchone()
    if row is None:
        return None
    return CachedResponse(
        url=row["url"],
        final_url=row["final_url"],
        status=int(row["status"]),
        content_type=row["content_type"],
        etag=row["etag"],
        last_modified=row["last_modified"],
        body=row["body"],
        sha256=row["sha256"],
        fetched_at=row["fetched_at"],
    )


def put(conn: sqlite3.Connection, entry: CachedResponse) -> None:
    conn.execute(
        "INSERT INTO responses (url, final_url, status, content_type, etag,"
        " last_modified, body, sha256, fetched_at) VALUES (?,?,?,?,?,?,?,?,?)"
        " ON CONFLICT(url) DO UPDATE SET final_url=excluded.final_url,"
        " status=excluded.status, content_type=excluded.content_type,"
        " etag=excluded.etag, last_modified=excluded.last_modified,"
        " body=excluded.body, sha256=excluded.sha256, fetched_at=excluded.fetched_at",
        (
            entry.url,
            entry.final_url,
            entry.status,
            entry.content_type,
            entry.etag,
            entry.last_modified,
            entry.body,
            entry.sha256,
            entry.fetched_at,
        ),
    )


def touch(conn: sqlite3.Connection, url: str) -> None:
    """Record that a conditional request confirmed the stored copy is current."""
    conn.execute("UPDATE responses SET fetched_at = ? WHERE url = ?", (_now(), url))


def get_robots(conn: sqlite3.Connection, host: str) -> str | None:
    row = conn.execute("SELECT body FROM robots WHERE host = ?", (host,)).fetchone()
    return None if row is None else str(row["body"])


def put_robots(conn: sqlite3.Connection, host: str, body: str) -> None:
    conn.execute(
        "INSERT INTO robots (host, body, fetched_at) VALUES (?,?,?)"
        " ON CONFLICT(host) DO UPDATE SET body=excluded.body, fetched_at=excluded.fetched_at",
        (host, body, _now()),
    )
