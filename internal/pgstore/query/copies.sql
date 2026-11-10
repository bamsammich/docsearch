-- Copying and moving a document between libraries, through
-- internal/repository/postgres.
--
-- A copy reads one library and writes another. A policy compares each row
-- with the single library app.library_id names, so the transaction switches
-- that setting between statements and every statement still answers to the
-- policies: a reader role where it reads, an editor role where it writes.

-- name: SetLibrary :exec
SELECT set_config('app.library_id', sqlc.arg(library_id)::text, true);

-- Whether the transaction's user edits the library it names.
-- name: EditsLibrary :one
SELECT (current_setting('app.library_id', true) = ANY (app_libraries('editor')))::boolean
  AS edits;

-- name: CopySource :one
SELECT doc_id, title, source_path, source_kind, sha256, status
  FROM documents WHERE doc_id = $1;

-- name: ReadyDocIDWithDigest :one
SELECT doc_id FROM documents WHERE sha256 = $1 AND status = 'ready' LIMIT 1;

-- A crawled site's cached responses, a batch at a time. The policy shows
-- them only to an editor, so a reader's copy finds none and the target
-- library fetches the site itself.
-- name: SiteResponses :many
SELECT url, final_url, status, content_type, etag, last_modified, body, sha256,
       fetched_at
  FROM responses
 WHERE starts_with(url, sqlc.arg(prefix)::text) AND url > sqlc.arg(after)::text
 ORDER BY url
 LIMIT sqlc.arg(batch);

-- A response the target library already holds is newer than the copy's.
-- name: CopyResponse :exec
INSERT INTO responses (
  library_id, url, final_url, status, content_type, etag, last_modified, body,
  sha256, fetched_at
) VALUES (current_setting('app.library_id', true), $1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (library_id, url) DO NOTHING;

-- name: EnqueueCopy :one
INSERT INTO ingest_jobs (
  library_id, requested_by, source_path, title, force, status, created_at,
  updated_at, copy_from, copy_doc_id, copy_as, move
) VALUES (
  current_setting('app.library_id', true), current_setting('app.user_id', true),
  $1, $2, false, 'queued', now(), now(), $3, $4, $5, $6
)
RETURNING id;

-- name: MoveOfJob :one
SELECT COALESCE(copy_from, '')::text AS copy_from,
       COALESCE(copy_doc_id, '')::text AS copy_doc_id, move
  FROM ingest_jobs WHERE id = $1;

-- A finished job's error says what part of the request did not happen.
-- name: NoteJob :exec
UPDATE ingest_jobs SET error = sqlc.arg(note)::text WHERE id = sqlc.arg(id);
