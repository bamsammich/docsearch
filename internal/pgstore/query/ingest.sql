-- Writes the ingest service makes, through internal/repository/postgres.
--
-- Reads the server makes live in documents.sql, chunks.sql and jobs.sql. The
-- split is by who runs the statement, not by which table it names: the worker
-- writes a document incrementally and the server must not see it until the
-- last statement here flips its status.
--
-- Every statement runs in a transaction that opened with
-- SET LOCAL app.user_id, so row-level security scopes it to one library. A
-- read therefore names no user: the policy is where that is decided, and a
-- second copy of the filter in 45 queries is 45 places for it to be wrong.
-- A write names user_id because a row has to carry its owner, and the policy
-- then refuses a value that disagrees with the session.

-- name: ReadyDocumentWithDigest :one
SELECT doc_id, title FROM documents WHERE sha256 = $1 AND status = 'ready';

-- name: CountDocumentChunks :one
SELECT COUNT(*) FROM chunks WHERE doc_id = $1;

-- name: DocIDForSourcePath :one
SELECT doc_id FROM documents WHERE source_path = $1;

-- name: DocIDsWithPrefix :many
SELECT doc_id FROM documents WHERE doc_id LIKE $1;

-- A document is born 'ingesting' with no chunk count and no timestamp, so
-- every read path, which filters on status = 'ready', passes over it until
-- MarkDocumentReady runs.
-- name: InsertDocument :exec
INSERT INTO documents (
  user_id, doc_id, title, format, source_path, source_kind, sha256,
  page_count, chunk_count, status, ingested_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULL, 'ingesting', NULL);

-- name: InsertChunk :exec
INSERT INTO chunks (
  user_id, doc_id, ordinal, section, page_start, page_end, printed_page_start,
  image_count, kind, url, fragment, heading_path, text
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- A page arrives again when a site is re-crawled, so the write is an upsert
-- rather than an insert that has to be preceded by a delete.
-- name: UpsertPage :exec
INSERT INTO pages (user_id, doc_id, page, text) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, doc_id, page) DO UPDATE SET text = EXCLUDED.text;

-- name: InsertIndexTerm :exec
INSERT INTO index_terms (user_id, doc_id, term, section) VALUES ($1, $2, $3, $4);

-- The moment a document becomes visible to search.
-- name: MarkDocumentReady :exec
UPDATE documents
   SET status = 'ready', chunk_count = $1, ingested_at = $2, warnings = $3
 WHERE doc_id = $4;

-- Run in the same transaction as MarkDocumentReady, so a document can never
-- be searchable while the job that wrote it still reads as running.
-- name: CompleteJob :exec
UPDATE ingest_jobs
   SET status = 'done', phase = NULL, doc_id = $1, error = NULL, warnings = $2,
       lease_until = NULL, updated_at = now()
 WHERE id = $3;

-- Ordering matters, because every one of these tables references documents:
-- deleting the document row first is refused by the foreign key. Chunks go
-- first for the same reason pages and index terms do.
-- name: DeleteDocumentChunks :exec
DELETE FROM chunks WHERE doc_id = $1;

-- name: DeleteDocumentPages :exec
DELETE FROM pages WHERE doc_id = $1;

-- name: DeleteDocumentIndexTerms :exec
DELETE FROM index_terms WHERE doc_id = $1;

-- name: DeleteDocumentRow :exec
DELETE FROM documents WHERE doc_id = $1;

-- Verification reads every column, because what it checks is whether the
-- columns agree with each other.
-- name: DocumentChunks :many
SELECT ordinal, section, page_start, page_end, printed_page_start,
       image_count, kind, url, fragment, heading_path, text
  FROM chunks
 WHERE doc_id = $1
 ORDER BY ordinal;

-- name: DocumentByID :one
SELECT doc_id, title, format, source_kind, status, page_count, chunk_count, warnings
  FROM documents WHERE doc_id = $1;

-- name: IndexTermSections :many
SELECT DISTINCT section FROM index_terms WHERE doc_id = $1 ORDER BY section;

-- Entries, not sections: the verify report counts what the back-of-book
-- index holds, and many entries point at one section.
-- name: IndexTermCount :one
SELECT COUNT(*) FROM index_terms WHERE doc_id = $1;

-- Subtree semantics, the same clause ChunksInSection uses: an index entry
-- pointing at chapter 4 refers to the whole chapter.
-- name: SectionHasChunks :one
SELECT EXISTS(
  SELECT 1 FROM chunks
   WHERE doc_id = $1 AND (chunks.section = $2 OR chunks.section LIKE $2 || '.%'));
