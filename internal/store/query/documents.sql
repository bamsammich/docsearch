-- name: ListReadyDocuments :many
SELECT doc_id, title, format, source_kind, page_count, chunk_count, warnings
  FROM documents
 WHERE status = 'ready'
 ORDER BY doc_id;

-- name: DocumentStatus :one
SELECT status FROM documents WHERE doc_id = ?;

-- name: SchemaVersion :one
SELECT version FROM schema_version LIMIT 1;
