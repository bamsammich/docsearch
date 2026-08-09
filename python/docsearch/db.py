"""SQLite storage: schema, connections, pragmas.

One SQLite file is the entire storage layer. WAL is mandatory, not optional --
the worker writes while the server reads, and rollback-journal mode deadlocks
them against each other.
"""

from __future__ import annotations

import sqlite3
from dataclasses import dataclass
from pathlib import Path

BUSY_TIMEOUT_MS = 5000


class SchemaError(Exception):
    """The database schema is not one this build can operate on."""


class SchemaOutdatedError(SchemaError):
    def __init__(self, found: int | None, required: int) -> None:
        self.found = found
        self.required = required
        seen = "unversioned" if found is None else f"version {found}"
        super().__init__(
            f"database is at {seen}, this build requires version {required}. "
            f"Run `docsearch migrate` to upgrade it."
        )


class SchemaTooNewError(SchemaError):
    def __init__(self, found: int, required: int) -> None:
        self.found = found
        self.required = required
        super().__init__(
            f"database is at version {found}, newer than the version {required} this build "
            f"understands. Upgrade docsearch rather than downgrading the database: an older "
            f"build cannot know what a newer one changed, and writing to it risks corrupting "
            f"data it does not understand."
        )


class SchemaMigrationError(SchemaError):
    def __init__(self, found: int | None, target: int, problems: list[str]) -> None:
        self.found = found
        self.target = target
        self.problems = problems
        seen = "unversioned" if found is None else f"version {found}"
        super().__init__(
            f"cannot migrate from {seen} to version {target}; the version was NOT recorded. "
            + " ".join(problems)
        )


#: Bumped whenever the schema changes in a way a reader must know about.
#:
#: Column presence is not sufficient. Adding a column is visible by inspection,
#: but changing what an existing column *means* -- index_terms.section holding
#: section numbers rather than page numbers, chunks.section becoming the
#: authoritative chunk key -- is invisible to any structural check while
#: silently changing what queries return. The Go server asserts this value at
#: readiness and refuses to serve a database it was not built against.
SCHEMA_VERSION = 4

#: Human-readable history, so a version mismatch can be diagnosed without
#: reading the git log.
SCHEMA_HISTORY = {
    1: "initial: documents, ingest_jobs, chunks + FTS5, pages, index_terms",
    2: "index_terms.section replaces page; chunks gains section, printed_page_start, image_count",
    3: "documents.warnings and ingest_jobs.warnings (JSON StructureReport); "
    "ingest_jobs.permanent distinguishes deterministic failure from exhaustion",
    4: "chunks.kind marks self-declared keyword-reference families so search "
    "can deprioritise them without deleting them",
}

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
  ingested_at  TEXT,
  warnings     TEXT              -- JSON StructureReport; NULL until ingest completes
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
  permanent     INTEGER NOT NULL DEFAULT 0,  -- failed deterministically; never retried
  cancel_req    INTEGER NOT NULL DEFAULT 0,
  error         TEXT,
  warnings      TEXT,
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
  kind               TEXT NOT NULL DEFAULT 'prose',
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

CREATE TABLE IF NOT EXISTS schema_version (
  version     INTEGER NOT NULL,
  applied_at  TEXT NOT NULL
);
"""


def connect(
    db_path: str | Path, *, create: bool = True, allow_outdated: bool = False
) -> sqlite3.Connection:
    """Open ``db_path`` with the pragmas both processes must agree on.

    ``create`` initialises a database that does not exist yet. It never
    migrates one that does. Opening used to imply migrating, and because every
    read path opens the database, `list`, `verify` and `jobs` all rewrote the
    recorded version -- so running an older build against a newer database
    stamped it backwards, and the newer server then refused to serve a
    database that was perfectly sound. Migration is `docsearch migrate`.
    """
    path = Path(db_path)
    fresh = not path.exists()
    if create:
        path.parent.mkdir(parents=True, exist_ok=True)
    elif fresh:
        raise FileNotFoundError(f"database does not exist: {path}")

    conn = sqlite3.connect(path, isolation_level=None, timeout=BUSY_TIMEOUT_MS / 1000)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute(f"PRAGMA busy_timeout={BUSY_TIMEOUT_MS}")
    conn.execute("PRAGMA foreign_keys=ON")
    conn.execute("PRAGMA synchronous=NORMAL")

    if create and fresh:
        # Initialising an empty file is not a migration: there is no existing
        # data whose meaning could be misread.
        conn.executescript(SCHEMA)
        _stamp(conn, SCHEMA_VERSION)
        return conn

    found = schema_version(conn)
    if found is not None and found > SCHEMA_VERSION:
        conn.close()
        raise SchemaTooNewError(found, SCHEMA_VERSION)
    if not allow_outdated and found != SCHEMA_VERSION:
        conn.close()
        raise SchemaOutdatedError(found, SCHEMA_VERSION)
    return conn


#: Columns added after the initial schema. CREATE TABLE IF NOT EXISTS leaves an
#: existing table untouched, so new columns need an explicit backfill.
_ADDED_COLUMNS = (
    ("documents", "warnings", "TEXT"),
    ("ingest_jobs", "warnings", "TEXT"),
    ("ingest_jobs", "permanent", "INTEGER NOT NULL DEFAULT 0"),
    ("chunks", "kind", "TEXT NOT NULL DEFAULT 'prose'"),
)


@dataclass(slots=True)
class MigrationResult:
    from_version: int | None
    to_version: int
    columns_added: list[str]


def _stamp(conn: sqlite3.Connection, version: int) -> None:
    conn.execute("DELETE FROM schema_version")
    conn.execute(
        "INSERT INTO schema_version (version, applied_at) VALUES (?, datetime('now'))",
        (version,),
    )


def _postconditions(conn: sqlite3.Connection) -> list[str]:
    """What must actually hold before a version may be recorded.

    A stamp is a claim that the database matches the code. Writing it without
    checking makes the claim unfalsifiable: the readiness gate then passes on a
    database that only says it migrated, and the failure surfaces later as a
    query error instead of at the gate.
    """
    problems: list[str] = []
    have = {
        r["name"]
        for r in conn.execute("SELECT name FROM sqlite_master WHERE type IN ('table','view')")
    }
    for table in REQUIRED_TABLES:
        if table not in have:
            problems.append(f"table {table} is missing")
    for table, column, _decl in _ADDED_COLUMNS:
        if table not in have:
            continue
        cols = {r["name"] for r in conn.execute(f"PRAGMA table_info({table})")}
        if column not in cols:
            problems.append(f"column {table}.{column} is missing")
    if "index_terms" in have:
        cols = {r["name"] for r in conn.execute("PRAGMA table_info(index_terms)")}
        if "section" not in cols:
            # The v2 change was semantic, not additive: the column held printed
            # page numbers and now holds section numbers. Nothing recovers one
            # from the other without the source document, so this cannot be
            # migrated in place -- and the index is regenerable, so it need not
            # be. Refusing beats stamping a version the data does not match.
            problems.append(
                "index_terms has no 'section' column, so this database predates the change "
                "from page references to section references. No transformation recovers "
                "section numbers from page numbers without the source documents. The index "
                "is fully regenerable: delete it and re-ingest the library."
            )
    return problems


def migrate(conn: sqlite3.Connection) -> MigrationResult:
    """Bring an existing database up to ``SCHEMA_VERSION``.

    Idempotent, and the only thing in the system that writes the version.
    """
    before = schema_version(conn)
    if before is not None and before > SCHEMA_VERSION:
        raise SchemaTooNewError(before, SCHEMA_VERSION)

    conn.executescript(SCHEMA)
    added: list[str] = []
    for table, column, decl in _ADDED_COLUMNS:
        existing = {r["name"] for r in conn.execute(f"PRAGMA table_info({table})")}
        if column not in existing:
            conn.execute(f"ALTER TABLE {table} ADD COLUMN {column} {decl}")
            added.append(f"{table}.{column}")

    problems = _postconditions(conn)
    if problems:
        raise SchemaMigrationError(before, SCHEMA_VERSION, problems)
    _stamp(conn, SCHEMA_VERSION)
    return MigrationResult(before, SCHEMA_VERSION, added)


def schema_version(conn: sqlite3.Connection) -> int | None:
    """The schema version recorded in the database, or None if unrecorded."""
    try:
        row = conn.execute("SELECT version FROM schema_version LIMIT 1").fetchone()
    except sqlite3.OperationalError:
        return None
    return int(row["version"]) if row else None


def schema_present(conn: sqlite3.Connection) -> bool:
    """True when every required table exists."""
    rows = conn.execute("SELECT name FROM sqlite_master WHERE type IN ('table','view')").fetchall()
    have = {r["name"] for r in rows}
    return all(t in have for t in REQUIRED_TABLES)


#: Resolve an index-term section reference to the chunks it covers.
#:
#: The match is component-wise, never a bare string prefix. A reference to
#: chapter "4" covers "4" and "4.1", and must not touch "41" or "43.6.1" --
#: LIKE '4%' would sweep in both. The trailing dot is what makes it a boundary.
#:
#: Coverage metrics are structurally blind to getting this wrong: over-matching
#: *raises* the number of resolved joins, so an "unjoinable = 0" check moves in
#: the reassuring direction while the results get worse. Precision needs its own
#: test. The index-term boost must resolve sections through this same rule.
SECTION_MATCH_SQL = "(chunks.section = ? OR chunks.section LIKE ? || '.%')"


def chunks_in_section(conn: sqlite3.Connection, doc_id: str, section: str) -> list[int]:
    """Chunk ids covered by ``section``, including its descendants."""
    rows = conn.execute(
        f"SELECT id FROM chunks WHERE doc_id = ? AND {SECTION_MATCH_SQL} ORDER BY ordinal",
        (doc_id, section, section),
    ).fetchall()
    return [r["id"] for r in rows]


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
