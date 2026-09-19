# go-pdfium spike: pass, with rules for the port

Date: 2026-09-19 · Library: `github.com/klippa-app/go-pdfium` v1.20.3, WebAssembly
mode (wazero, no cgo) · Host: Apple Silicon, arm64
Method: dump the primitives the PDF adapter reads from each engine, replay
both through the unchanged Python pipeline, compare chunk output, then run the
committed retrieval eval against an index built from each. Code in
`spike/pdfium/`; `spike/pdfium/run.sh` reproduces every figure here in about
40 seconds.

The test documents are three product manuals and four journal papers, named
here by letter.

## Verdict: go-pdfium can replace PyMuPDF

The phase 01 gate asked whether PDFium matches PyMuPDF's outline, fonts and
figure counts on large manuals at acceptable speed. It does, on all three
manuals, which between them take the adapter's three structure paths. Chunk
output is identical in structure, and retrieval on the labelled query set is
identical.

| manual | pages | structure source | chunks | heading paths | chunk starts |
|---|---|---|---|---|---|
| Manual A | 70 | font heuristic | 35 / 35 | identical | identical |
| Manual B | 458 | embedded outline | 281 / 281 | identical | identical |
| Manual C | 1,848 | printed contents | 944 / 944 | identical | identical |

| eval, n=53 | @1 | @3 | @8 | @20 |
|---|---|---|---|---|
| PyMuPDF index | 42% | 53% | 68% | 79% |
| PDFium index | 42% | 53% | 68% | 79% |

No query changed rank. The full eval output differs in two places: one
relevance score (0.63 against 0.62) and one chunk's image count (8 against 5).

The four journal papers match less closely; see
[Journal papers](#journal-papers). None of their gaps blocks the port, and
each has a named cause.

All of it depends on five rules in the port, below. Without them the same
library refused Manual C outright.

## The replay harness is exact

The adapter reads five things from PyMuPDF: text lines with a position and a
font size, plain page text, image placements, the outline, and the metadata
title. `replay.py` stands a fake document in for `pymupdf.open` and feeds the
pipeline a JSON dump of those primitives. Replaying PyMuPDF's own dump
reproduces the real adapter's chunks byte for byte on the three manuals (35,
281 and 944 chunks, the counts an existing index holds), so a difference in a
PDFium replay comes from PDFium's primitives and nothing else.

## Five rules the port needs

PDFium reports runs of same-font text rather than MuPDF's lines, so the port
builds lines itself. Each rule fixed a measured failure.

**1. Use the rendered font size, not the nominal one.** Manual C sets its text
at a nominal size of 1, scaled by the text matrix. With nominal sizes the
adapter finds no font hierarchy and refuses the document.
`FontInformation.RenderedSize` matches MuPDF, occasionally 0.1 apart in
rounding (17.3 against 17.2), which changes no decision.

**2. Convert to top-down coordinates and clip to the page.** PDFium's `Top`
and `Bottom` are PDF user space, origin at the bottom left. MuPDF also clips
extraction to the page and PDFium does not: Manual C carries a stray "1" below
the bottom edge of 1,846 pages, which normalizes to a boilerplate `#` and then
deletes every page number from the printed contents.

**3. Keep vertical text out of line grouping.** Some journal PDFs print an
"Author Manuscript" watermark down the margin of every page. Its runs are
taller than any line, overlap every row, and got glued onto body lines, which
moved Paper B's body size from 10 to 9. A run more than twice as tall as it is
wide becomes a line of its own, as MuPDF reports it.

**4. Build lines the way MuPDF does.**

| step | rule | failure it fixed |
|---|---|---|
| band | runs whose vertical extents overlap by half form a row | none alone; the basis for the rest |
| shared extent | lines in a row with the same font size share one top and bottom | Manual C's "2.2." and its title had tops a point apart and landed in different contents rows |
| only same size | a 12pt heading and 9pt text in the other column keep their own tops | Paper D's "Discussion" heading was keyed to the table beside it |
| split | a gap over 1em, or over 0.45em between two same-font runs | contents rows merged "7.17." into its title |
| join | a space at every font or size change, and between same-font runs only past 0.15em or where PDFium reported whitespace | Manual C's footer arrives one glyph per run and read as "P h o n e" |

The 0.45em same-font split works because PDFium already merges continuous
same-font text into one run: two same-font runs side by side sit apart only
because the text jumped to a tab stop or a table cell. It sits between Manual
C's footer, whose word gaps reach 0.35em, and its contents rows, where a long
section number leaves about 0.48em before the title.

**5. Let the contents parser accept "7.17. Title" as one cell.** No gap
threshold separates every contents row's number from its title, because the
longest section numbers leave almost no gap. Line grouping alone recovers 445
of Manual C's 827 contents rows. Splitting a merged first cell in the parser
(`spike/pdfium/tocvariant.py`) recovers all 827, identical in section, title
and page. The port rewrites this parser anyway, and the change leaves
PyMuPDF's result unchanged.

## Journal papers

| paper | pages | structure source | chunks | heading paths | section text similarity |
|---|---|---|---|---|---|
| Paper A | 46 | embedded outline | 60 / 60 | identical | 0.967 |
| Paper B | 74 | embedded outline | 85 / 86 | identical | 0.982 |
| Paper C | 37 | embedded outline | 32 / 30 | identical | 0.928 |
| Paper D | 24 | font heuristic | 38 / 38 | 0.90 overlap | 0.868 |

Section text similarity compares each heading's full text between engines,
weighted by length. The manuals score 0.996 to 0.998 on the same measure.

Three causes account for the gaps:

- **Two-column reading order.** The adapter sorts a page's lines by their
  top, which interleaves the two columns of a journal page line by line with
  either engine. PDFium's tops differ slightly from MuPDF's line boxes, so the
  interleaving differs. The words in a section are the same; their order is
  not. A line builder working from PDFium's per-character loose boxes, which
  follow font metrics as MuPDF's do, should close this. Unverified.
- **Images drawn through pattern fills.** Paper C's first page has five images
  that MuPDF reports with no object number. PDFium finds no image objects on
  that page at all: the images are painted by pattern fills on two paths, and
  MuPDF renders pattern contents when collecting image info while PDFium's
  object API cannot see inside a pattern. Swapping PyMuPDF's placements into
  PDFium's dump restores 30 of Paper C's 36 missing chunk image counts but
  not its two missing chunks, so those come from reading order. A producer
  quirk, not seen in the manuals.
- **One heading each way on Paper D.** PyMuPDF marks a body sentence as a
  heading; PDFium marks the real heading it missed. PDFium's reading is the
  better one here.

## What still differs on the manuals

| difference | measured | effect |
|---|---|---|
| section text | similarity 0.996 to 0.998 | spacing ("3. x") and order within table rows; counts of `(` and `)` match on every document but Manual C, which gains two pairs |
| image identity | Manual C keeps 2,055 figures against 2,061 | PDFium exposes no object numbers, so identity is a hash of the image bytes, which also collapses byte-identical images stored twice |
| boilerplate lines | Manual B strips 5 against 4, Manual C 6 against 5 | no change to any heading path or chunk start |

## Speed and size

| | Manual A, 70 pp | Manual B, 458 pp | Manual C, 1,848 pp |
|---|---|---|---|
| PyMuPDF | 0.60 s | 4.12 s | 11.64 s |
| PDFium, WebAssembly | 0.15 s | 0.95 s | 4.80 s |

PDFium also pays 1.1 seconds once per process to compile the WebAssembly
module. Peak memory on Manual C was 585 MB against PyMuPDF's 566 MB. The spike
binary is 15 MB, with the 5.7 MB PDFium module embedded.

## Not measured

- **Other CPU architectures.** wazero compiles to native code only on amd64
  and arm64 and interprets everything else much more slowly; every timing
  above is arm64.
- **A memory cap on the sandbox.** wazero can limit a module's memory pages;
  the spike ran without a limit, so the worst case for a hostile PDF is
  unmeasured.
- **Other PDFs.** Seven documents. The line thresholds were tuned on Manual C
  and hold on the other manuals; the port's parity tests should run against
  every PDF the corpus holds and grow with it.
