#!/bin/sh
# Reproduce the go-pdfium spike: dump both engines' primitives for every PDF in
# the library, compare them through the Python pipeline, then run the labelled
# retrieval eval against a grandMA2 index built from each engine.
#
#   spike/pdfium/run.sh [LIBRARY_DIR]
#
# Run from the repository root. Outputs land in var/spike/, which is ignored.
set -eu

lib=${1:-"$HOME/Documents/docsearch-library"}
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

# The committed query set targets grandMA2 by section and QLC+ by heading.
for engine in pymupdf rendered; do
  uv run python spike/pdfium/replay.py index "$lib/grandMA2_Light_Manual.pdf" \
    "$out/grandMA2_Light_Manual.$engine.json" "$out/eval-$engine.db" "$lib/qlcplus-manual.md" \
    > /dev/null
  go run ./cmd/docsearch-eval --db "$out/eval-$engine.db" > "$out/eval-$engine.txt"
  grep "ALL " "$out/eval-$engine.txt" | sed "s/^ */$engine: /"
done
diff "$out/eval-pymupdf.txt" "$out/eval-rendered.txt" | grep -c '^[<>]' | sed 's/^/eval output lines that differ: /' || true
