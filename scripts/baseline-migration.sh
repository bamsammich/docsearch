#!/usr/bin/env bash
# Wraps python/docsearch/schema.sql in the annotations goose needs, as
# internal/schema/migrations/00005_baseline.sql.
#
# The schema is one file: sqlc types its queries against the original, and a
# baseline that drifted would have the two disagreeing. internal/schema tests
# for that.
#
# The baseline must stay idempotent. An index the Python pipeline created
# carries no goose record, so the first time goose sees one it applies the
# baseline again; CREATE TABLE IF NOT EXISTS is what makes that harmless.
#
# The down side is written here rather than derived, because nothing derives
# a teardown from a schema. It drops in dependency order: the triggers that
# write the full-text index, then the index itself, then the tables that
# reference documents, then documents and everything standalone.
#
# Retired with the Python pipeline, when the migration becomes the original.
set -euo pipefail

out=internal/schema/migrations/00005_baseline.sql

{
  printf '%s\n' '-- Generated from python/docsearch/schema.sql by `mise run generate`.'
  printf '%s\n' '-- Do not edit: the schema is one file, and sqlc types its queries against'
  printf '%s\n' '-- the original. Version 5 is the floor this baseline establishes; every'
  printf '%s\n' '-- later change is its own numbered migration.'
  printf '%s\n' '--'
  printf '%s\n' '-- +goose Up'
  printf '%s\n' '-- +goose StatementBegin'
  cat python/docsearch/schema.sql
  printf '%s\n' '-- +goose StatementEnd'
  printf '%s\n' ''
  printf '%s\n' '-- +goose Down'
  printf '%s\n' '-- +goose StatementBegin'
  printf '%s\n' '-- The triggers first: each one writes chunks_fts, and dropping the table'
  printf '%s\n' '-- they write to before they are gone leaves a trigger referring to nothing.'
  printf '%s\n' 'DROP TRIGGER IF EXISTS chunks_au;'
  printf '%s\n' 'DROP TRIGGER IF EXISTS chunks_ad;'
  printf '%s\n' 'DROP TRIGGER IF EXISTS chunks_ai;'
  printf '%s\n' 'DROP TABLE IF EXISTS chunks_fts;'
  printf '%s\n' '-- Then the tables that reference documents, then documents itself.'
  printf '%s\n' 'DROP TABLE IF EXISTS index_terms;'
  printf '%s\n' 'DROP TABLE IF EXISTS pages;'
  printf '%s\n' 'DROP TABLE IF EXISTS chunks;'
  printf '%s\n' 'DROP TABLE IF EXISTS documents;'
  printf '%s\n' 'DROP TABLE IF EXISTS ingest_jobs;'
  printf '%s\n' 'DROP TABLE IF EXISTS schema_version;'
  printf '%s\n' '-- +goose StatementEnd'
} > "$out"
