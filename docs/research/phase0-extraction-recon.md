# Phase 0 — Extraction reconnaissance: grandMA2 Light Manual

Date: 2026-08-02
Project: docsearch
Source: `~/Downloads/grandMA2_Light_Manual.pdf` (33.5 MB, PDF 1.3/1.4)
Producer chain: `wkhtmltopdf 0.12.2.1` → `Mac OS X 10.12.6 Quartz PDFContext`
Title metadata: "Manual of MA Lighting International GmbH" · Version 3.8, 2019-12-11

## 1. Text layer — PRESENT, no OCR required

`pdffonts` lists 16 embedded CID TrueType fonts, all `emb=yes uni=yes`
(Roboto Regular/Bold/Italic, ArialMT, Lato-Light, TradeGothicLTCom, Tahoma,
plus one subsetted `font0000000023bbdeb9`). Every page has a real extractable
text layer. **`ocrmypdf` is not needed.**

## 2. Outline tree — ABSENT

- `doc.get_toc()` returns **0 entries**.
- Internal link graph is also unusable: `get_links()` across all 1,848 pages
  yields only 26 `LINK_URI` and 8 `LINK_LAUNCH`, and **zero `LINK_GOTO`**.

The wkhtmltopdf → Quartz round-trip dropped all bookmarks and internal anchors.
Per the spec this is a "report, do not silently degrade" condition. However, two
independent replacement structure sources exist and both are clean.

### 2a. Front TOC (pages 3–28) — RECONSTRUCTS PERFECTLY

Naive `get_text("text")` on TOC pages is scrambled (a three-column layout emits
section numbers and page numbers interleaved as pairs, then all titles in a
separate run). Grouping positioned lines into 4pt y-bands and sorting by x
recovers it exactly:

- **827 `(section, title, printed_page)` triples**
- Depth distribution: **56 at depth 1, 381 at depth 2, 390 at depth 3**
- Spans `1. New in the Manual` (p28) through `56. Index` (p1800)

This is functionally equivalent to the missing `get_toc()`, with numbering.

### 2b. Font-size hierarchy — CLEAN AND CORROBORATING

Only 20 distinct `(size, font, flags)` combinations exist in the whole document —
programmatic HTML styling, not hand layout.

| Size | Role | Lines | Notes |
|---|---|---|---|
| 16.5 | H1 chapter | 64 | number and title emitted as *separate* lines (`'1.'`, `'New in the Manual'`) — must be joined |
| 15.0 | H2 | 438 | numbered inline: `'2.1.   About this Manual'` |
| 14.2 | H3 | 983 | unnumbered: `'Front Panel'`, `'Navigation in the Help'` |
| 13.5 | H4 | 1300 | unnumbered: `'Set the Cursor in the Command Line'` |
| 12.8 | — | 2 | body-text spillover, **not** a heading level |
| 11.2 | TOC entries | 237 | front-matter only |
| 10.5 Bold | **NOT a heading** | 3033 | inline emphasis + labels (`'Warning:'`, `'Hint:'`, `'Example:'`) |
| 10.5 Regular | body | 1.25M chars | dominant body text |
| 9.8 / 8.8 | boilerplate | 9230 | running footer/header |

**Trap:** 10.5-Bold has 3,033 lines and looks heading-shaped. Treating it as a
heading level would shatter the document into thousands of fragments. The
heading levels are size-driven only: 16.5 / 15.0 / 14.2 / 13.5.

The TOC covers depth 1–3 authoritatively (with numbering); font detection adds
depth 4 and exact intra-page y-positions. They are complementary, not redundant.

## 3. Corpus size

| Measure | Raw | Boilerplate stripped |
|---|---|---|
| Characters | 1,927,881 | 1,438,988 |
| Word/punct atoms | 417,024 | 309,091 |
| **Est. tokens** (atoms × 1.3) | **~542,000** | **~402,000** |

**Boilerplate is 25.9% of all tokens.** Stripping it is not cosmetic — a quarter
of the BM25 term mass is a repeated address and phone number.

## 4. Running headers / footers — 6 lines × ~1,846 pages

Detected by normalizing digits to `#` and counting page occurrences:

| Occurrences | Line |
|---|---|
| 1846 | `© 2019 MA Lighting Technology GmbH - Dachdeckerstr. 16 - 97297 Waldbüttelbrunn - Germany` |
| 1846 | `Phone +49 5251 688865-30 - tech.support@malighting.com - www.malighting.com` |
| 1846 | `<n> of 1847` |
| 1846 | `Version 3.8 – 2019-12-11` |
| 1847 | `English` |
| ~1846 | `grandMA2 User Manual - <current chapter>` |

Note the copyright/phone lines appear *first* in extracted reading order but are
visually at the page bottom — detect by repetition frequency, not position.

The `grandMA2 User Manual - <chapter>` footer encodes the current chapter and is
a useful cross-check on the derived heading path, but must not enter chunk text.

## 5. Page numbering offset

TOC lists chapter 1 at printed page 28; it is PDF page 29. The footer on PDF
page 211 reads `210 of 1847`. **printed = PDF − 1**, consistently.

## 6. Back-of-book index (pages 1801–1848) — PARSEABLE, but not `term … page`

1,800 lines recovered; **1,750 parse (97.2%)**. All 50 non-parsing lines are page
boilerplate (`grandMA2 User Manual - Index`, `56. Index`) — no real entry fails.

**The references are SECTION numbers, not page numbers:**

```
AllChaseExecutors keyword  10.2.14.
Audio In connector         6.9.
desk lamp                  6.5.    51.3.
active values              10.2.34.    16.5.
```

- 1,841 section refs total
- **1,841 / 1,841 (100%) resolve against the reconstructed front TOC**
- Multi-reference entries are common and must map to multiple rows

This conflicts with the spec's `index_terms(doc_id, term, page)` assumption of a
`term … page` pattern. It is fully recoverable: section → printed page via the
TOC, then printed page → PDF page via the +1 offset. The schema stays as written.

## 7. Extraction quality by page type

**Body prose — clean.** Pages 500, 1041, 1500 extract in correct reading order
with paragraph integrity intact.

**Figure-heavy pages — degraded, as expected.** 629 pages have ≥2 images and
<900 chars; 328 pages fall below 300 net chars after boilerplate stripping.
Example (p211): heading plus four short lines, including `Location Key Update`,
a caption orphaned from the image it labels. These produce sub-100-token blocks
that the merge-forward rule must absorb.

**Table pages — flattened but not scrambled.** Page 95 (keyword reference table,
240 vector rules) extracts as:

```
'Clear Clear' / 'Clear' / 'ClearAll' / 'Releases everything and clears the'
/ 'programmer.' / 'No example' / 'Copy' / 'Copy' / 'Copies source object to
destination' / 'object.' / 'Copy Cue 1 At 5'
```

Reading order is row-major and correct, but cell boundaries are lost and the
first column duplicates (`'Clear Clear'`). Semantically recoverable for search;
not faithfully reconstructable as a table.

**TOC pages — scrambled** under naive extraction (see 2a); requires positional
reconstruction.

## 8. Structural granularity — an open design question

~402k net tokens across an estimated ~2,000–3,000 structural blocks gives a mean
of roughly 150–200 tokens per section — well below the spec's ~800-token chunk
figure. The document's own structure is much finer-grained than the target chunk
size, so the merge-forward rule will carry most of the shaping load, and the
~1,200-token subdivision rule will rarely fire.

## Tooling notes

- `poppler` is not in the mise registry; installed via Homebrew for `pdffonts`.
- Token counts use an offline estimator (`\w+|[^\w\s]` atoms × 1.3) to avoid a
  network dependency at ingest time. All spec thresholds are approximate ("~").
- Obsidian MCP was not available this session; this file is the local fallback
  per the knowledge-cache rules and should be migrated when MCP is reachable.
