-- name: ListReadyDocuments :many
SELECT doc_id, title, format, source_kind, page_count, chunk_count, warnings,
       source_path, ingested_at
  FROM documents
 WHERE status = 'ready'
 ORDER BY doc_id;

-- name: DocumentStatus :one
SELECT status FROM documents WHERE doc_id = $1;

-- name: SchemaVersion :one
SELECT version FROM schema_version LIMIT 1;

-- A rename changes only what the document is called. The doc_id stays, so a
-- caller holding it still finds the document.
-- name: RenameDocument :execrows
UPDATE documents SET title = $2 WHERE doc_id = $1;

-- The libraries the transaction's user reads, their own first. A definer
-- function, because the request role cannot read library_grants.
-- name: ReadableLibraries :many
SELECT library_id::text AS library_id, kind::text AS kind,
       COALESCE(slug, '')::text AS slug, COALESCE(name, '')::text AS name,
       role::text AS role
  FROM readable_libraries();
