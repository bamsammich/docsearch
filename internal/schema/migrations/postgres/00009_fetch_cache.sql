-- Version 9: the crawl's response cache moves into the database.
--
-- A worker in a container loses a local file when it restarts, and surviving
-- a restart is most of what the cache is for: a cancelled crawl resumes, a
-- refresh sends conditional requests, and re-chunking a site makes no
-- requests at all. Beside the index rather than in a file, the cache is
-- backed up with everything else and reachable from whichever worker picks
-- the job up next.
--
-- robots is per user too, although a robots.txt is public and identical for
-- everybody. Which hosts someone crawled is not public, and an exception to
-- the isolation rule is worth more than the one request per user per host it
-- would save.
--
-- Neither table is partitioned, unlike chunks. A cache holds one crawl's
-- responses and is emptied by a schema change rather than kept for the life
-- of a library, so it does not reach the size where per-user statistics
-- changed the plans.
--
-- +goose Up
-- +goose StatementBegin
CREATE TABLE responses (
  user_id       TEXT NOT NULL REFERENCES users(user_id),
  url           TEXT NOT NULL,          -- normalized request URL
  final_url     TEXT NOT NULL,          -- after redirects; equals url when none
  status        INTEGER NOT NULL,
  content_type  TEXT,
  etag          TEXT,
  last_modified TEXT,
  body          BYTEA NOT NULL,
  sha256        TEXT NOT NULL,
  fetched_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (user_id, url)
);

CREATE TABLE robots (
  user_id    TEXT NOT NULL REFERENCES users(user_id),
  host       TEXT NOT NULL,
  body       TEXT NOT NULL,             -- empty where the host serves none
  fetched_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (user_id, host)
);

ALTER TABLE responses ENABLE ROW LEVEL SECURITY;
ALTER TABLE responses FORCE  ROW LEVEL SECURITY;
ALTER TABLE robots    ENABLE ROW LEVEL SECURITY;
ALTER TABLE robots    FORCE  ROW LEVEL SECURITY;

CREATE POLICY own ON responses USING (user_id = current_setting('app.user_id', true))
  WITH CHECK (user_id = current_setting('app.user_id', true));
CREATE POLICY own ON robots    USING (user_id = current_setting('app.user_id', true))
  WITH CHECK (user_id = current_setting('app.user_id', true));

GRANT SELECT, INSERT, UPDATE, DELETE ON responses, robots TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
REVOKE ALL ON responses, robots FROM docsearch_app;
DROP POLICY own ON robots;
DROP POLICY own ON responses;
DROP TABLE robots;
DROP TABLE responses;
-- +goose StatementEnd
