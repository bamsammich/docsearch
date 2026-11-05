-- name: PutUpload :exec
INSERT INTO uploads (library_id, key, filename, sha256, content)
VALUES (current_setting('app.library_id', true), $1, $2, $3, $4)
ON CONFLICT (library_id, key) DO UPDATE
  SET filename = EXCLUDED.filename, sha256 = EXCLUDED.sha256,
      content = EXCLUDED.content, uploaded_at = now();

-- name: GetUpload :one
SELECT key, filename, sha256, content FROM uploads WHERE key = $1;

-- name: CreateUploadLink :exec
INSERT INTO upload_links (token_hash, library_id, issued_by, expires_at)
VALUES ($1, current_setting('app.library_id', true), current_setting('app.user_id', true), $2);

-- Redeeming runs before any user is known, on a bare connection.
-- name: RedeemUploadLink :many
SELECT library_id::text AS library_id, COALESCE(issued_by, '')::text AS issued_by
  FROM redeem_upload_link(sqlc.arg(token_hash)::text);

-- name: UploadLinkOpen :one
SELECT upload_link_open(sqlc.arg(token_hash)::text)::boolean AS open;
