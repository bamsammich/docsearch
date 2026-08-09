
CREATE TABLE IF NOT EXISTS documents (
  doc_id       TEXT PRIMARY KEY,
  title        TEXT NOT NULL,
  format       TEXT NOT NULL,
  source_path  TEXT NOT NULL,   -- absolute path, or the canonical base URL of a site
  source_kind  TEXT NOT NULL DEFAULT 'file',  -- 'file' | 'site'
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
  url                TEXT,             -- page this chunk was read from; NULL for local files
  fragment           TEXT,             -- in-page anchor, without the leading '#'
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
