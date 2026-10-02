-- Chunk and page reads, through internal/repository/postgres.
--
-- Every statement runs in a transaction that named its user, so none of
-- these filters on user_id: row-level security is where the library is
-- chosen.

-- name: TopLevelHeadings :many
SELECT DISTINCT heading_path, ordinal FROM chunks
 WHERE doc_id = $1 AND heading_path <> ''
 ORDER BY ordinal;

-- name: OutlineRows :many
SELECT id, section, page_start, heading_path
  FROM chunks WHERE doc_id = $1 ORDER BY ordinal;

-- name: ChunkOrdinal :one
SELECT ordinal FROM chunks WHERE id = $1 AND doc_id = $2;

-- name: ContextChunks :many
SELECT id, ordinal, heading_path, section, page_start, image_count, url, fragment, text
  FROM chunks
 WHERE doc_id = $1 AND ordinal >= $2 AND ordinal <= $3
 ORDER BY ordinal;

-- name: PagesInRange :many
SELECT page, text FROM pages
 WHERE doc_id = $1 AND page >= $2 AND page <= $3
 ORDER BY page;

-- Section references resolve component-wise, never by bare string prefix: a
-- reference to "4" covers "4" and "4.1" and must not touch "41". The trailing
-- dot is what makes it a boundary. Kept identical to store.SectionCovers,
-- which the in-memory index-term boost applies by the same rule.
-- name: ChunksInSection :many
SELECT heading_path, text FROM chunks
 WHERE doc_id = $1 AND (chunks.section = $2 OR chunks.section LIKE $2 || '.%')
 ORDER BY ordinal;

-- name: ChunksMatchingHeading :many
SELECT heading_path, text FROM chunks
 WHERE doc_id = $1 AND lower(heading_path) LIKE '%' || lower($2) || '%'
 ORDER BY ordinal;

-- name: AllChunksInOrder :many
SELECT id, heading_path, text FROM chunks
 WHERE doc_id = $1 ORDER BY ordinal;
