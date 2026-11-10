-- Version 36: a document can be copied or moved into another library.
--
-- A copy is an ingest job in the target library that remembers where its
-- document came from. It re-reads the original, so extracted rows never
-- cross libraries. The job keeps the source's doc_id unless the person asked
-- for another, and a move removes the source in the transaction that
-- publishes the copy, while the person still edits the source library.
--
-- Nothing here needs a definer function. Starting a copy reads the source
-- and writes the target in one transaction that switches app.library_id
-- between statements, so the existing policies check a reader role on one
-- side and an editor role on the other.

-- +goose Up
ALTER TABLE ingest_jobs
  ADD COLUMN copy_from   text,
  ADD COLUMN copy_doc_id text,
  ADD COLUMN copy_as     text,
  ADD COLUMN move        boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE ingest_jobs
  DROP COLUMN move,
  DROP COLUMN copy_as,
  DROP COLUMN copy_doc_id,
  DROP COLUMN copy_from;
