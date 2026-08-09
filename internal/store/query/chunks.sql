-- name: TopLevelHeadings :many
SELECT DISTINCT heading_path FROM chunks
 WHERE doc_id = ? AND heading_path <> ''
 ORDER BY ordinal;

-- name: OutlineRows :many
SELECT id, section, page_start, heading_path
  FROM chunks WHERE doc_id = ? ORDER BY ordinal;

-- name: ChunkOrdinal :one
SELECT ordinal FROM chunks WHERE id = ? AND doc_id = ?;

-- name: ContextChunks :many
SELECT id, ordinal, heading_path, section, page_start, image_count, url, fragment, text
  FROM chunks
 WHERE doc_id = ? AND ordinal BETWEEN ? AND ?
 ORDER BY ordinal;

-- name: PagesInRange :many
SELECT page, text FROM pages
 WHERE doc_id = ? AND page BETWEEN ? AND ?
 ORDER BY page;

-- Section references resolve component-wise, never by bare string prefix: a
-- reference to "4" covers "4" and "4.1" and must not touch "41". The trailing
-- dot is what makes it a boundary. Kept identical to store.SectionMatchSQL,
-- which the in-memory index-term boost applies by the same rule.
-- name: ChunksInSection :many
SELECT heading_path, text FROM chunks
 WHERE doc_id = ? AND (chunks.section = ? OR chunks.section LIKE ? || '.%')
 ORDER BY ordinal;

-- name: ChunksMatchingHeading :many
SELECT heading_path, text FROM chunks
 WHERE doc_id = ? AND lower(heading_path) LIKE '%' || lower(?) || '%'
 ORDER BY ordinal;

-- name: AllChunksInOrder :many
SELECT id, heading_path, text FROM chunks
 WHERE doc_id = ? ORDER BY ordinal;
