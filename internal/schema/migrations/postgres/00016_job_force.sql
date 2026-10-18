-- Version 16: a job can force a re-read of bytes the index already holds.
--
-- Ingest skips bytes a ready document already holds, so a sync can send an
-- unchanged file cheaply. A forced job reads them again anyway, which is how
-- a change to extraction or grading reaches a document whose file never
-- changed. Every job already here was queued unforced.
--
-- +goose Up
-- +goose StatementBegin
ALTER TABLE ingest_jobs ADD COLUMN force BOOLEAN NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ingest_jobs DROP COLUMN force;
-- +goose StatementEnd
