-- Version 17: the app role may read the built-in account's partition by name.
--
-- Search names the BM25 index on the searching account's own partition,
-- because pg_textsearch 1.4.0 scores a query naming the parent index with the
-- first partition's statistics (upstream issue 552), which are the built-in
-- library's whichever account searches. Naming a partition's index needs
-- SELECT on that partition. create_account grants it for each partition it
-- makes, and migration 6 made chunks_default before create_account existed,
-- granting the parent table alone.
--
-- Row-level security still decides which rows a statement reaches: the
-- policy on chunks covers a read of any of its partitions through it.
--
-- +goose Up
-- +goose StatementBegin
GRANT SELECT ON chunks_default TO docsearch_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
REVOKE SELECT ON chunks_default FROM docsearch_app;
-- +goose StatementEnd
