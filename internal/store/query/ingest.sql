-- Writes the ingest service makes, through internal/repository/sqlite.
--
-- Reads the server makes live in documents.sql, chunks.sql and jobs.sql. The
-- split is by who runs the statement, not by which table it names: the worker
-- writes a document incrementally and the server must not see it until the
-- last statement here flips its status.

-- name: ReadyDocumentWithDigest :one
SELECT doc_id, title FROM documents WHERE sha256 = ? AND status = 'ready';

-- name: CountDocumentChunks :one
SELECT COUNT(*) FROM chunks WHERE doc_id = ?;

-- name: DocIDForSourcePath :one
SELECT doc_id FROM documents WHERE source_path = ?;

-- name: DocIDsWithPrefix :many
SELECT doc_id FROM documents WHERE doc_id LIKE ?;

-- A document is born 'ingesting' with no chunk count and no timestamp, so
-- every read path, which filters on status = 'ready', passes over it until
-- MarkDocumentReady runs.
-- name: InsertDocument :exec
INSERT INTO documents (
  doc_id, title, format, source_path, source_kind, sha256,
  page_count, chunk_count, status, ingested_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, 'ingesting', NULL);

-- name: InsertChunk :exec
INSERT INTO chunks (
  doc_id, ordinal, section, page_start, page_end, printed_page_start,
  image_count, kind, url, fragment, heading_path, text
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpsertPage :exec
INSERT OR REPLACE INTO pages (doc_id, page, text) VALUES (?, ?, ?);

-- name: InsertIndexTerm :exec
INSERT INTO index_terms (doc_id, term, section) VALUES (?, ?, ?);

-- The moment a document becomes visible to search.
-- name: MarkDocumentReady :exec
UPDATE documents
   SET status = 'ready', chunk_count = ?, ingested_at = ?, warnings = ?
 WHERE doc_id = ?;

-- Run in the same transaction as MarkDocumentReady, so a document can never
-- be searchable while the job that wrote it still reads as running.
-- name: CompleteJob :exec
UPDATE ingest_jobs
   SET status = 'done', phase = NULL, doc_id = ?, error = NULL, warnings = ?,
       lease_until = NULL, updated_at = datetime('now')
 WHERE id = ?;

-- Ordering matters. The AFTER DELETE trigger on chunks is what clears
-- chunks_fts, so chunks go through SQL rather than a bulk drop, and they go
-- first: deleting the document row first would take them with it through the
-- foreign key, without the trigger.
-- name: DeleteDocumentChunks :exec
DELETE FROM chunks WHERE doc_id = ?;

-- name: DeleteDocumentPages :exec
DELETE FROM pages WHERE doc_id = ?;

-- name: DeleteDocumentIndexTerms :exec
DELETE FROM index_terms WHERE doc_id = ?;

-- name: DeleteDocumentRow :exec
DELETE FROM documents WHERE doc_id = ?;
