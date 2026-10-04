-- Version 7: chunks become searchable.
--
-- One index over one expression, with the heading written twice before the
-- body. docs/research/postgres-spike.md measured why: FTS5's
-- bm25(chunks_fts, 1.0, 2.0) adds weighted term counts across columns before
-- saturating, under one inverse document frequency and one length, while two
-- indexes added together score each column separately and count a term
-- present in both at full strength twice. Two indexes cost six points at
-- depth 8 and eight points of self-label accuracy; writing the heading twice
-- inside one field reproduces FTS5's arithmetic and closed the gap.
--
-- The index is declared on the parent, so every partition gets one of its
-- own and each user's statistics cover that user's rows alone. A query must
-- order by this expression verbatim for the index to drive it.
--
-- 'simple' is the text search configuration the spike measured: no stemming
-- and no stop words, which is what FTS5 did.
--
-- +goose Up
-- +goose StatementBegin
CREATE INDEX chunks_bm25 ON chunks
  USING bm25 ((heading_path || ' ' || heading_path || ' ' || text))
  WITH (text_config='simple');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX chunks_bm25;
-- +goose StatementEnd
