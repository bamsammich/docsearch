-- The crawl's response cache, read and written by the worker through
-- internal/site/fetch/pgcache.
--
-- Nothing a search reads is here. The cache holds raw HTTP responses so that
-- a cancelled crawl resumes, a refresh can send conditional requests, and
-- re-chunking a site makes none.
--
-- Every statement runs in a transaction that opened with
-- SET LOCAL app.user_id, so a read names no user and the policy decides what
-- it sees. A write names user_id because a row has to carry its owner.

-- name: CachedResponse :one
SELECT url, final_url, status, content_type, etag, last_modified, body, sha256,
       fetched_at
  FROM responses WHERE url = $1;

-- A URL fetched again replaces what was stored for it, which is what makes a
-- refresh idempotent.
-- name: StoreResponse :exec
INSERT INTO responses (
  user_id, url, final_url, status, content_type, etag, last_modified, body,
  sha256, fetched_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (user_id, url) DO UPDATE SET
  final_url = EXCLUDED.final_url,
  status = EXCLUDED.status,
  content_type = EXCLUDED.content_type,
  etag = EXCLUDED.etag,
  last_modified = EXCLUDED.last_modified,
  body = EXCLUDED.body,
  sha256 = EXCLUDED.sha256,
  fetched_at = EXCLUDED.fetched_at;

-- A conditional request that came back 304 confirms the stored copy, so only
-- its age changes. The database's clock is good enough here, because nothing
-- measures how old a response is; a robots.txt is the opposite case.
-- name: TouchResponse :exec
UPDATE responses SET fetched_at = now() WHERE url = $1;

-- name: CachedRobots :one
SELECT body, fetched_at FROM robots WHERE host = $1;

-- The crawler supplies the time rather than the database, because the crawler
-- is what compares the file's age against the 24 hours RFC 9309 allows. Read
-- from one clock and written by another, the comparison would answer to the
-- skew between a worker and its database.
-- name: StoreRobots :exec
INSERT INTO robots (user_id, host, body, fetched_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, host) DO UPDATE SET
  body = EXCLUDED.body, fetched_at = EXCLUDED.fetched_at;
