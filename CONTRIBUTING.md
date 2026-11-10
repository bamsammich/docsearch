# Contributing

Building, testing and extending docsearch from a checkout. The [README](README.md) covers running it.

## Setup

```bash
mise install          # go 1.26, node 22, golangci-lint, buf, sqlc, mockery
mise run build        # the library UI, then docsearch-server, docsearch-worker, docsearch, docsearch-sync
```

`pdffonts` (poppler) is useful for inspecting a PDF's text layer and is not in
the mise registry — install it from your package manager.

## Checks

```bash
mise run setup        # Go modules and the git hooks
mise run check        # everything CI runs, before a push
mise run generate     # migrations snapshot, query layer, mocks and proto output
```

## Testing

```bash
go test ./...                                    # every package, and the integration suites
go run ./cmd/docsearch-eval --dsn "$DOCSEARCH_DSN"             # retrieval evaluation
go run ./cmd/docsearch-eval --dsn DSN --self-label             # measure any corpus, no query set
```

The retrieval harness and its query sets are described in [docs/retrieval.md](docs/retrieval.md#evaluating-an-index).

## The library UI

The UI is a Svelte app in `web/`, built into `internal/webui/dist` and
embedded with `go:embed`. A binary built without it answers `/` with a note
to run `mise run web`. `npm run dev` in `web/` serves it with hot reload and
proxies the API to a server on `127.0.0.1:8765`.

## Adding a format adapter

One module plus one registry entry. The chunker is untouched.

1. Write `internal/adapter/yourformat`, exporting an `Extract` that returns
   a `domain.Extraction`.
2. Emit `Block`s: `HeadingPath` (full ancestry, root-first), `Locator`
   (a page for paginated formats, an offset otherwise), and `Text`.
   Set `Section` only if the format carries authoritative numbering.
3. Register the suffix in `internal/adapter`.

The chunker reads only the normalized intermediate and never learns which
adapter produced it. If your format's structure cannot be derived, **raise**
rather than falling back to fixed windows — silent degradation to token
windows is how a document quietly becomes unsearchable.
