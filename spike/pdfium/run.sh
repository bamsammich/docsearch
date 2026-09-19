#!/bin/sh
# Reproduce the go-pdfium spike: dump both engines' primitives for every PDF in
# a directory, compare them through the Python pipeline, then run the labelled
# retrieval eval against an index built from each engine.
#
#   EVAL_PDF=... EVAL_MD=... spike/pdfium/run.sh LIBRARY_DIR
#
# EVAL_PDF and EVAL_MD are the two documents the committed query set in
# tests/retrieval/queries.json targets. Run from the repository root. Outputs
# land in var/spike/, which is ignored.
set -eu

lib=${1:?usage: EVAL_PDF=... EVAL_MD=... spike/pdfium/run.sh LIBRARY_DIR}
eval_pdf=${EVAL_PDF:?set EVAL_PDF to the PDF the query set targets}
eval_md=${EVAL_MD:?set EVAL_MD to the Markdown document the query set targets}
out=var/spike
mkdir -p "$out"

(cd spike/pdfium && go build -o ../../$out/pdfium-spike .)

find "$lib" -name '*.pdf' | sort | while read -r pdf; do
  f=$(basename "$pdf" .pdf)
  uv run python spike/pdfium/replay.py dump "$pdf" "$out/$f.pymupdf.json"
  "$out/pdfium-spike" -pdf "$pdf" -out "$out/$f" > /dev/null
  uv run python spike/pdfium/replay.py compare "$pdf" \
    "$out/$f.pymupdf.json" "$out/$f.rendered.json" > "$out/$f.compare.json"
  uv run python spike/pdfium/replay.py summary "$out/$f.compare.json"
done

eval_name=$(basename "$eval_pdf" .pdf)
for engine in pymupdf rendered; do
  uv run python spike/pdfium/replay.py index "$eval_pdf" \
    "$out/$eval_name.$engine.json" "$out/eval-$engine.db" "$eval_md" \
    > /dev/null
  go run ./cmd/docsearch-eval --db "$out/eval-$engine.db" > "$out/eval-$engine.txt"
  grep "ALL " "$out/eval-$engine.txt" | sed "s/^ */$engine: /"
done
diff "$out/eval-pymupdf.txt" "$out/eval-rendered.txt" | grep -c '^[<>]' | sed 's/^/eval output lines that differ: /' || true
