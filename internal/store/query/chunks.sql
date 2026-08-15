-- name: TopLevelHeadings :many
SELECT DISTINCT heading_path FROM chunks
 WHERE doc_id = ? AND heading_path <> ''
 ORDER BY ordinal;

-- name: OutlineRows :many
SELECT id, section, page_start, heading_path
  FROM chunks WHERE doc_id = ? ORDER BY ordinal;

-- name: ChunkOrdinal :one
SELECT ordinal FROM chunks WHERE id = ? AND doc_id = ?;

-- Written as two comparisons rather than BETWEEN ? AND ?: sqlc's SQLite engine
-- keeps the placeholders in the emitted SQL but does not count them as
-- parameters, so the generated call passes the doc_id alone and the driver
-- rejects it at runtime for the wrong argument count.
-- name: ContextChunks :many
SELECT id, ordinal, heading_path, section, page_start, image_count, url, fragment, text
  FROM chunks
 WHERE doc_id = ? AND ordinal >= ? AND ordinal <= ?
 ORDER BY ordinal;

-- name: PagesInRange :many
SELECT page, text FROM pages
 WHERE doc_id = ? AND page >= ? AND page <= ?
 ORDER BY page;

-- Section references resolve component-wise, never by bare string prefix: a
-- reference to "4" covers "4" and "4.1" and must not touch "41". The trailing
-- dot is what makes it a boundary. Kept identical to store.SectionCovers, which
-- the in-memory index-term boost applies by the same rule, and to
-- docsearch.db.SECTION_MATCH_SQL, which is this same clause on the Python side.
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
