-- Settings an administrator changed, kept for a docsearch started without
-- docsearch.yaml. The request role reads them; set_setting writes them.

-- name: StoredSettings :many
SELECT key, value FROM settings ORDER BY key;

-- name: SetSetting :exec
SELECT set_setting(sqlc.arg(key)::text, sqlc.narg(value)::text);
