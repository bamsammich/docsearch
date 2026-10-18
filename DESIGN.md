---
name: docsearch Library
description: The library UI shows how much of each document an agent can actually reach.
colors:
  bg: "#f4f5f3"
  surface: "#ffffff"
  surface-2: "#eceeeb"
  line: "#dcdfdb"
  line-strong: "#c5c9c4"
  ink: "#17191b"
  muted: "#586068"
  accent: "#2350c0"
  accent-weak: "#e8eefb"
  good: "#23683f"
  good-bg: "#e1f0e6"
  good-fill: "#3f8f5e"
  warn: "#7a5000"
  warn-bg: "#faefd2"
  warn-fill: "#d29a1e"
  bad: "#a3261b"
  bad-bg: "#f9e4e1"
  lost: "#c27b72"
  on-bad: "#ffffff"
  code-bg: "#1a1c1f"
  code-fg: "#e4e6e8"
  code-key: "#9fc2ef"
  code-str: "#d9c48a"
  bg-dark: "#121417"
  surface-dark: "#1a1d21"
  surface-2-dark: "#22262b"
  line-dark: "#2d3238"
  line-strong-dark: "#3b4148"
  ink-dark: "#e8eaed"
  muted-dark: "#a1a8b0"
  accent-dark: "#87a8ff"
  accent-weak-dark: "#1d2740"
  good-dark: "#80cb9e"
  good-bg-dark: "#15291c"
  good-fill-dark: "#4f9d6c"
  warn-dark: "#e5bb5d"
  warn-bg-dark: "#2f260e"
  warn-fill-dark: "#c99325"
  bad-dark: "#f3897e"
  bad-bg-dark: "#351a19"
  lost-dark: "#a3564e"
  on-bad-dark: "#1a1d21"
typography:
  headline:
    fontFamily: "Libre Franklin, Helvetica Neue, Arial, sans-serif"
    fontSize: "24px"
    fontWeight: 600
    lineHeight: 1.25
    letterSpacing: "-0.015em"
  title:
    fontFamily: "Libre Franklin, Helvetica Neue, Arial, sans-serif"
    fontSize: "20px"
    fontWeight: 600
    lineHeight: 1.3
    letterSpacing: "-0.01em"
  title-sm:
    fontFamily: "Libre Franklin, Helvetica Neue, Arial, sans-serif"
    fontSize: "17px"
    fontWeight: 600
    lineHeight: 1.4
  brand:
    fontFamily: "Libre Franklin, Helvetica Neue, Arial, sans-serif"
    fontSize: "18px"
    fontWeight: 700
    letterSpacing: "-0.015em"
  lead:
    fontFamily: "Libre Franklin, Helvetica Neue, Arial, sans-serif"
    fontSize: "16px"
    fontWeight: 400
    lineHeight: 1.55
  body:
    fontFamily: "Libre Franklin, Helvetica Neue, Arial, sans-serif"
    fontSize: "15px"
    fontWeight: 400
    lineHeight: 1.55
  body-sm:
    fontFamily: "Libre Franklin, Helvetica Neue, Arial, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.55
  meta:
    fontFamily: "Libre Franklin, Helvetica Neue, Arial, sans-serif"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.5
  label:
    fontFamily: "Libre Franklin, Helvetica Neue, Arial, sans-serif"
    fontSize: "12px"
    fontWeight: 600
    letterSpacing: "0.06em"
  chip:
    fontFamily: "Libre Franklin, Helvetica Neue, Arial, sans-serif"
    fontSize: "12px"
    fontWeight: 600
  mono:
    fontFamily: "Red Hat Mono, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "0.94em"
    fontWeight: 400
  code:
    fontFamily: "Red Hat Mono, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.55
rounded:
  strip: "4px"
  badge: "5px"
  control: "7px"
  inset: "8px"
  card: "10px"
  page: "12px"
  pill: "14px"
spacing:
  xs: "4px"
  sm: "6px"
  md: "8px"
  lg: "12px"
  xl: "14px"
  2xl: "18px"
  3xl: "24px"
  gutter: "28px"
components:
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.surface}"
    typography: "{typography.body-sm}"
    rounded: "{rounded.control}"
    padding: "7px 13px"
  button-secondary:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    typography: "{typography.body-sm}"
    rounded: "{rounded.control}"
    padding: "7px 13px"
  button-secondary-hover:
    backgroundColor: "{colors.surface-2}"
  button-danger:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.bad}"
    rounded: "{rounded.control}"
    padding: "7px 13px"
  button-danger-solid:
    backgroundColor: "{colors.bad}"
    textColor: "{colors.on-bad}"
    rounded: "{rounded.control}"
    padding: "7px 13px"
  button-small:
    rounded: "{rounded.control}"
    padding: "4px 10px"
  badge-good:
    backgroundColor: "{colors.good-bg}"
    textColor: "{colors.good}"
    typography: "{typography.chip}"
    rounded: "{rounded.badge}"
    padding: "2px 8px"
  badge-degraded:
    backgroundColor: "{colors.warn-bg}"
    textColor: "{colors.warn}"
    typography: "{typography.chip}"
    rounded: "{rounded.badge}"
    padding: "2px 8px"
  badge-unusable:
    backgroundColor: "{colors.bad-bg}"
    textColor: "{colors.bad}"
    typography: "{typography.chip}"
    rounded: "{rounded.badge}"
    padding: "2px 8px"
  tab:
    textColor: "{colors.muted}"
    padding: "10px 12px 11px"
  tab-active:
    textColor: "{colors.ink}"
  tab-count:
    backgroundColor: "{colors.surface-2}"
    textColor: "{colors.muted}"
    rounded: "{rounded.card}"
    padding: "1px 6px"
  tab-count-alert:
    backgroundColor: "{colors.bad-bg}"
    textColor: "{colors.bad}"
  filter-group:
    backgroundColor: "{colors.surface-2}"
    rounded: "{rounded.inset}"
    padding: "3px"
  filter-option:
    textColor: "{colors.muted}"
    rounded: "6px"
    padding: "5px 11px"
  filter-option-active:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
  input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "7px 10px"
  coverage-strip:
    backgroundColor: "{colors.surface-2}"
    rounded: "{rounded.strip}"
    height: "8px"
  coverage-strip-thin:
    rounded: "3px"
    height: "5px"
  problem-row:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.card}"
    padding: "13px 15px 14px"
  healthy-row:
    backgroundColor: "{colors.surface}"
    typography: "{typography.body-sm}"
    padding: "9px 15px"
  healthy-row-selected:
    backgroundColor: "{colors.accent-weak}"
  document-page:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.page}"
    padding: "22px 26px 24px"
  finding:
    backgroundColor: "{colors.surface-2}"
    rounded: "{rounded.inset}"
    padding: "12px 14px"
  finding-degraded:
    backgroundColor: "{colors.warn-bg}"
  finding-unusable:
    backgroundColor: "{colors.bad-bg}"
  job-card:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.card}"
    padding: "14px 16px"
  job-status-running:
    backgroundColor: "{colors.accent-weak}"
    textColor: "{colors.accent}"
    rounded: "{rounded.badge}"
    padding: "2px 8px"
  job-status-stalled:
    backgroundColor: "{colors.warn-bg}"
    textColor: "{colors.warn}"
    rounded: "{rounded.badge}"
    padding: "2px 8px"
  job-status-failed:
    backgroundColor: "{colors.bad-bg}"
    textColor: "{colors.bad}"
    rounded: "{rounded.badge}"
    padding: "2px 8px"
  job-status-done:
    backgroundColor: "{colors.good-bg}"
    textColor: "{colors.good}"
    rounded: "{rounded.badge}"
    padding: "2px 8px"
  job-status-queued:
    backgroundColor: "{colors.surface-2}"
    textColor: "{colors.muted}"
    rounded: "{rounded.badge}"
    padding: "2px 8px"
  phase-bar:
    backgroundColor: "{colors.surface-2}"
    rounded: "2px"
    height: "4px"
  search-hit:
    backgroundColor: "{colors.surface}"
    typography: "{typography.body-sm}"
    rounded: "{rounded.card}"
    padding: "12px 16px 13px"
  search-sources:
    backgroundColor: "{colors.surface}"
    typography: "{typography.body-sm}"
    rounded: "{rounded.card}"
    padding: "14px 16px"
  gate:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.page}"
    padding: "20px 22px 22px"
    width: "460px"
  example-chip:
    backgroundColor: "{colors.surface}"
    typography: "{typography.meta}"
    rounded: "{rounded.pill}"
    padding: "2px 10px"
  raw-response:
    backgroundColor: "{colors.code-bg}"
    textColor: "{colors.code-fg}"
    typography: "{typography.code}"
    rounded: "{rounded.card}"
    padding: "14px 16px"
  toast:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.surface}"
    typography: "{typography.body-sm}"
    rounded: "{rounded.inset}"
    padding: "9px 14px"
---

# Design System: docsearch Library

## Overview

**Creative North Star: "The Coverage Readout"**

Every document in the library is measured, not described. The one shared visual is the coverage strip, a three-part bar showing how much of a document an agent can reach by chapter, how much it can reach only with unreliable chapters, and how much never reached the index. It appears on problem rows, on the open document, on finished ingest jobs and beside search results, so the same reading carries from queue to library to diagnosis to retrieval. Everything else stays quiet so that the strip and the verdict are what the eye lands on.

The ground is neutral and cool: an off-white page in light mode, near-black in dark, with white (or one step lighter) surfaces separated by hairline borders rather than shadows. Type is a single sans family, Libre Franklin, at modest sizes and weights; hierarchy comes from weight (400 to 600) and from muted grey against near-black ink, not from big display sizes. Red Hat Mono appears only where the text is a machine identifier: document and chunk IDs, file names and source URLs. Density is moderate on problems and tight on healthy documents: problems get a card each with their strip and finding, healthy documents collapse into a compact list.

The system refuses two things the user has rejected. It does not dress the interface as a physical library or any other costume metaphor, and it does not become an anonymous developer console where every status is an equal-weight pill. State is honest and loud only where something is wrong.

**Key Characteristics:**
- The coverage strip is the signature; green, hatched amber and dusty red belong to it and to verdicts.
- One blue accent, used for focus, selection, links and live (running) state.
- Primary actions are near-black ink, not blue.
- Status is always an icon plus a word; colour is never the only carrier.
- Flat surfaces with hairline borders; only the open document page lifts.
- One authored motion: opening a document grows its strip to full length.
- Prose capped at 72ch; numbers in tabular figures.

## Colors

A cool neutral ground with one blue accent and three reserved status hues that mean coverage and verdict, nothing else.

### Primary
- **Signal Blue** (`accent`; dark `accent-dark`): focus rings, the selected problem row's border, links and link buttons, the running job status and the current phase bar. It marks where the person is and what is happening right now.
- **Signal Blue Wash** (`accent-weak`): the selected healthy row, the running status chip, the input focus halo, text selection and matched terms in search hits and their context.

### Secondary
- **Reachable Green** (`good-fill`): the "by chapter" segment of the coverage strip and the top bar of the brand mark. `good` and `good-bg` carry the Good verdict and the Done job status.
- **Caution Amber** (`warn-fill`): the hatched "chapters unreliable" segment and stalled phase bars. `warn` and `warn-bg` carry the Degraded verdict, degraded findings and stalled jobs.
- **Dusty Red** (`bad`, `bad-bg`): the Unusable verdict, failed jobs and phases, errors, destructive actions and the alert count on tabs. **On Red** (`on-bad`) is the label colour on a solid red fill: white in light mode, the dark surface in dark mode, where the red is light.
- **Lost Rose** (`lost`): the "not indexed" segment of the strip. A muted, greyed rose, strong enough to show as a segment against the Mist track in both themes, desaturated enough to read as absence rather than alarm.

### Neutral
- **Cool Paper** (`bg`): the page ground.
- **White Surface** (`surface`): cards, rows, the document page, inputs, the active filter option.
- **Mist** (`surface-2`): strip track, filter and segmented-control troughs, neutral findings, hover fills, neutral counts.
- **Hairline** (`line`) and **Rule** (`line-strong`): card borders and dividers; `line-strong` for control borders, dashed notices, the k cut-off rule and completed phase bars.
- **Ink** (`ink`): text, the active tab underline and the primary button fill.
- **Slate** (`muted`): secondary text, metadata, inactive tabs, labels.
- **Terminal** (`code-bg`, `code-fg`, `code-key`, `code-str`): the raw MCP response view only. The same values serve both themes.

Dark mode swaps every role to its `-dark` token; it follows `prefers-color-scheme` unless the page carries an explicit `data-theme` of `light` or `dark`.

### Named Rules
**The Reserved Hues Rule.** Green, amber and red mean coverage, verdict, job status, error or destruction. They are never decoration, never a brand colour, never a category tint.

**The One Blue Rule.** Blue is the only accent and it means focus, selection, a link or live work. A primary button is ink, so blue never competes with the action.

**The Never Colour Alone Rule.** Every verdict, job state and active phase pairs its hue with an icon and a word. The coverage strip carries a full `aria-label` and a numeric key.

## Typography

**Body Font:** Libre Franklin (with Helvetica Neue, Arial, sans-serif), weights 400, 500, 600, 700
**Mono Font:** Red Hat Mono (with ui-monospace, SF Mono, Menlo, monospace), weights 400, 500

Both families are self-hosted, bundled with the app as Latin subsets in exactly these weights; the UI makes no font-CDN request.

**Character:** A plain, slightly warm grotesque used at working sizes, with weight doing the hierarchy. The mono is a tag for machine text, not a voice.

### Hierarchy
- **Headline** (600, 24px, 1.25, -0.015em, balanced wrap): the open document's title. 21px below 640px.
- **Title** (600, 20px, -0.01em): page heads, the empty state, the sign-in gate (22px there).
- **Title Small** (600, 17px): panel headings such as the Add document form.
- **Brand** (700, 18px, -0.015em): the wordmark beside the three-bar mark.
- **Lead** (400, 16px): the document's consequence headline ("Agents reach 28 of 240 pages."), and the search query field.
- **Body** (400, 15px, 1.55): the default; the library summary line.
- **Body Small** (400 to 600, 14px): rows, findings, buttons, inputs, notes. Most of the interface lives here.
- **Meta** (400, 13px): sources, row metadata, hints, the coverage key, the k cut-off, field labels (600).
- **Label** (600, 12px, 0.06em, uppercase, `muted`): section headings in the list, on the document page and over the search sources column ("Needs attention", "Findings", "Index problems", "Warnings", "Where these came from"). Each heads a section of content; none sits above another heading. **Chip** (600, 12px, no tracking or case change) sets badges, status chips and counts.
- **Mono** (400, 0.94em of its context): IDs, chunk numbers, file names, URLs, environment variables. Identifiers the server quotes in backticks inside its own sentences (findings, evidence, warnings, inspection details) render as inline code at 0.9em.

### Named Rules
**The One Family Rule.** Libre Franklin sets everything a person reads. Red Hat Mono is only for IDs, sources, URLs, backtick-quoted identifiers, and the raw response view.

**The Measure Rule.** Running prose (job notes, hit text, context, findings) caps at 72ch.

**The Tabular Rule.** Any count, page number, rank, timing or ratio uses tabular figures so columns of numbers do not shimmer as they update.

## Layout

A single centred shell (max 1320px, 16px side padding respecting safe-area insets, 18px top and 88px bottom). The header carries the brand mark and the signed-in identity; beneath it a tab bar (Library, Ingest, Search preview), each with a count, sits on a hairline with the Add document button at its right end.

- **Library:** two columns, the list at 340 to 440px and the open document filling the rest, 28px gutter. The document column is sticky. Below 960px the list goes full width and opening a document turns the page into a full-screen sheet (content max 680px) with a Back to library button.
- **Ingest:** the add form at up to 360px beside the job list, collapsing to one column below 1080px.
- **Search preview:** a query bar in one bordered row (query, document, section filter, k, Search), wrapping to two columns below 1080px and stacking below 640px. Results take the page width: hits in a column up to 860px beside a sticky "Where these came from" column up to 300px, 28px gutter; below 1080px the column stacks under the hits and stops sticking.
- **Below 640px:** tabs become an equal three-column grid with short labels, and Add document moves above them at full width.

Spacing runs on a small, slightly irregular step set (4, 6, 8, 12, 14, 18, 24, 28px). Rows in a list sit 8px apart; sections on the document page sit 24px apart; columns sit 28px apart.

Breakpoints: 1080px (ingest and search results go single column), 960px (library sheet), 640px (phone).

## Elevation & Depth

Flat by default. Depth comes from the Cool Paper ground against White Surfaces and hairline borders. One ambient shadow exists and it is reserved for things that float above the list: the open document page and the toast. Selected and active states use rings, not lift.

### Shadow Vocabulary
- **Float** (`box-shadow: 0 1px 2px rgba(20,24,28,.05), 0 6px 18px rgba(20,24,28,.06)`; dark `0 1px 2px rgba(0,0,0,.4), 0 6px 18px rgba(0,0,0,.25)`): the document page, the toast.
- **Raised option** (`box-shadow: 0 1px 2px rgba(0,0,0,.08)`): the active option inside a filter or segmented control.
- **Selection ring** (`box-shadow: 0 0 0 1px` accent, with an accent border): the open problem row.
- **Focus halo** (`box-shadow: 0 0 0 3px` accent-weak, with an accent border): focused text inputs.

### Named Rules
**The Only The Page Floats Rule.** Rows, cards and panels sit flat on the ground. The open document is the one surface with the Float shadow.

## Shapes

Softly rounded rectangles throughout, graduated by size: 4px for the strip, 5px for badges and status chips, 7px for buttons and inputs, 8px for findings and control troughs, 10px for rows, job cards and hits, 12px for the document page and panels. Example query chips (14px) and tab counts (10px) are the only full pills. Borders are 1px hairlines; dashed borders mean "not a record": the ingesting notice, the file drop zone, and the k cut-off rule.

The brand mark is three rounded coverage bars stacked: a full green bar, an ink bar ending in a pale rose segment, and a short ink bar.

## Components

### Coverage Strip (signature)
The measure of a document, shown in two sizes.
- **Full** (8px tall, 4px radius, 2px gaps): on the open document, full width under the headline, followed by a key (13px, `muted`, 10px rounded swatches) that names each non-zero segment with its count: "by chapter", "chapters unreliable", "not indexed".
- **Thin** (5px tall, 3px radius, 1px gaps): on problem rows, on finished ingest jobs, and per document in the search sources column.
- **Segments**, proportional by flex: reachable by chapter in solid `good-fill`; reachable but chapters unreliable in a 135° hatch of `warn-fill` stripes (3px on, 3px off) over `warn-bg`; not indexed in `lost`. Zero segments are omitted; each present segment keeps a 3px minimum.
- **Accessibility:** the strip is `role="img"` with a sentence label giving all three counts and the total.
- **Signature motion:** opening a document grows its full strip from the left: a `clip-path` inset runs from 62% to 0 over 0.6s on `cubic-bezier(.16,1,.3,1)`, and the key settles in (opacity 0.2 to 1, 2px drop, 0.45s ease-out). The animation has no fill mode, so wherever it does not run the strip rests whole; reduced motion turns it off. The page itself does not move.

**The One Moment Rule.** The strip growing is the system's only authored motion. Everything else is a 0.15s state transition or a progress fill.

### Verdict Badges
- **Style:** inline icon (13px, stroke 2.1) plus word, 12px 600, 5px radius, tinted background with the matching dark text.
- **Good:** check icon, `good` on `good-bg`. **Degraded:** warning triangle, `warn` on `warn-bg`. **Unusable:** stop circle, `bad` on `bad-bg`.
- Badges appear on problem rows, the document page, finished jobs, the verdict legend, beside degraded documents in search results, and on every document in the search sources column. Healthy rows carry no badge; their place in the "Fully searchable" list says it.
- A document graded Good that still has gaps (pages not indexed, unreliable chapters, or index problems) wears Degraded: the badge follows what an agent loses, not the grade alone.

### Buttons
- **Shape:** gently rounded (7px), 1px border, 14px 500 text, 7px icon gap, 16px stroked icons.
- **Primary:** ink fill with surface text (Add document, Search this document, Sign in). Hover drops opacity to 0.88.
- **Secondary:** white surface, `line-strong` border, ink text; hover fills with Mist.
- **Danger:** secondary with red text (Remove). **Danger solid:** red fill with an On Red label, used only to confirm a removal inside its inline confirm block.
- **Quiet:** borderless and transparent until hover (Dismiss).
- **Small:** 4px by 10px, 13px; used for remedies and job actions.
- **Link button:** accent text, underlined (3px offset), for Show context and Show all.
- **Disabled:** 0.5 opacity, not-allowed cursor. Transitions on background and border, 0.15s.

### Tabs
- Muted 500 text, 2px transparent underline sitting on the bar's hairline; hover darkens to ink; the current tab is ink, 600, with an ink underline.
- Each tab carries a count pill (12px 600, `surface-2`/`muted`); a count that needs attention switches to `bad-bg`/`bad`.

### Filters and Segmented Controls
- A Mist trough (8px radius, 3px padding) holding borderless options (14px 500, muted). The pressed option becomes a white surface with ink text and the Raised option shadow. Filter options carry their counts in tabular figures.
- The library's verdict filter (All, Needs attention, Good) and the add form's Site/File switch share this form. The title filter beside it is a search input with a leading search icon.

### Disclosures
- A summary line in muted 500 text led by the system's drawn chevron (14px, same stroke set), which turns 90° over 0.15s ease-out when open. The browser's own triangle is hidden.
- Used for the verdict legend, the document's Details, and Recently finished jobs.

### Inputs / Fields
- **Style:** white surface, `line-strong` border, 7px radius, 7px by 10px padding, 14px. Labels 13px 600 above, 5px gap; hints 13px muted; errors 13px `bad`.
- **Focus:** accent border plus the 3px accent-weak halo. Every other focusable element gets a 2px accent outline at 2px offset.
- **Disabled:** Mist background.

### Problem Rows versus Healthy Rows
- **Problem row** (one card per document needing attention, 8px apart): white surface, hairline border, 10px radius. Top line is the title (600) with its verdict badge at the right; then the thin coverage strip; then the finding, bolded in the verdict's text colour, followed by the consequence in muted 14px. Hover strengthens the border; the open row gets an accent border and ring.
- **Healthy rows** (one bordered list, 10px radius, rows divided by hairlines): 14px title with right-aligned muted metadata ("site · 214 pp · 1 warning") in tabular figures, 9px by 15px. Hover fills with Mist; the open row fills with accent-weak.
- Problems always lead. The summary line above both reads "**9 of 12** fully searchable · **2** degraded · **1** unusable", in tabular figures; each count stays on one line with its label when the line wraps, and the separators are muted. Zero counts are left out.

### Document Page
In order: title (Headline) with verdict badge at the right; the consequence headline (Lead); the full coverage strip and key; **Findings**; **Index problems** and **Warnings** as muted bulleted lists holding only what no finding already states; **Outline** as a numbered list with right-aligned page numbers, six shown with a Show all link; a collapsed **Details** disclosure (source in mono, extent, added date, ID in mono); and an action row above a hairline: Search this document (primary), Re-ingest, Re-chunk from cache for sites, and Remove pushed to the far right. Remove opens an inline red confirm block stating exactly what agents lose. Re-ingest leaves the action row when a finding already offers it.

**Finding blocks.** One block per problem, 12px apart, each an 8px-radius block tinted by the document's verdict (warn-bg for Degraded, bad-bg for Unusable). A block carries its name in 600 and the verdict's text colour, one or more evidence lines at 72ch, an "**Agents lose:**" line, and optionally a remedy row: a small button (Inspect structure or Re-ingest) and a 13px muted hint. Inspect expands a white inset (6px radius) listing the structure sources the file offers. The structural findings (no addressable sections, a flat hierarchy) merge into one block with one Inspect remedy; chunks without headings form that block when no structural finding does; pages that never reached the index form their own block with Re-ingest, even with no finding code behind them. The server's sentences render as written, with backtick identifiers set as mono code.

**The One Problem, One Block Rule.** A problem is stated once, in its block, with its evidence, its cost and its remedy together. Lists below the findings hold only what no block covers.

### Job Cards and Phase Bars
- **Card:** white surface, hairline border, 10px radius, 14px by 16px. A failed card swaps its border for a `bad-bg` ring. Top line: title (600) and a status chip, a 12px icon (stroke 2.2, 4px gap) before a 12px 600 word: Running (play) and Cancelling (cross) in accent, Stalled (warning) in amber, Failed (stop) in red, Done (check) in green, Queued (plus) and Cancelled (cross) in Mist. Beneath, a muted meta line: source in mono, job ID in mono, elapsed or finish time.
- **Phase bars:** five equal columns (discover, fetch, extract, chunk, index), each a 4px track with a fill scaled from the left (0.5s linear), then an icon and phase name (12px), then a tabular value (time taken or `cur / tot`). Done: `line-strong` fill, check icon. Now: accent fill and accent label, play icon. Stalled: amber fill and label, warning icon. Failed: full red fill and label, stop icon. Each phase carries a screen-reader state.
- **Notes and results:** stalled jobs get an amber note on when the lease ends; failed jobs get the error in bold red then the reason; finished jobs show the thin coverage strip, verdict badge and headline, with Open in library.

### Search Preview Ranked List
- **Hit:** a 10px-radius card with a 30px rank column (600, muted, tabular). The body shows the document title (13px muted, with a badge if not Good), the heading path joined by › in 600 (or "No heading" in red), a location line (mono URL with fragment, or page range; chunk ID in mono; Show context link), and the chunk text at 72ch, clamped to six lines, with matched terms marked in Signal Blue Wash. Show context reveals neighbouring chunks behind a 2px hairline left rule, neighbours in muted.
- **k cut-off marker:** after the last hit, a centred 13px muted sentence flanked by dashed `line-strong` rules: "k = 8. 3 more chunks matched; the agent never sees them." When fewer than k matched it says so instead.
- **Server note:** an amber-washed note above results when a degraded document is among them or a section filter cannot work.
- **Where these came from:** a flat white column (10px radius, hairline border, 14px by 16px) under a Label heading. One entry per document, 14px apart: the title as a 600 ink link-button, then a 13px muted line with the hit count in tabular figures and the verdict badge, then the thin coverage strip. Selecting a title opens that document in the library.
- Results can be read ranked or grouped by document, or as the raw MCP response in the Terminal palette.

### Sign-in Gate
A single panel (12px radius, hairline border, up to 460px, 64px below the header) with a Title at 22px, 14px body copy, a muted line on where the token lives, and a primary button with the key icon. The copy and the button follow the server's auth mode: sign in with the OIDC issuer named by host, sign in with a docsearch account when docsearch is its own issuer (password mode), or paste the server token into a field (token mode). An OIDC server without a browser client gets an explanation naming the setting, with its identifiers in mono; a failed sign-in shows the error in red with Try again.

## Do's and Don'ts

### Do:
- **Do** put a coverage strip wherever a document's reachability is the point: problem rows, the document page, finished jobs, the search sources column.
- **Do** pair every verdict, job state and active phase with an icon and a word.
- **Do** keep primary actions ink and reserve blue for focus (2px outline, 2px offset), selection, links and running work.
- **Do** lead with problems and collapse healthy documents into the dense list.
- **Do** cap running prose at 72ch and set every number in tabular figures.
- **Do** keep motion to the 0.6s strip grow on opening a document, 0.15s state transitions and 0.5s phase fills, and drop all of it under `prefers-reduced-motion: reduce`.
- **Do** state each document problem once, as a finding block with its evidence, what agents lose and its remedy.
- **Do** define every colour role for both themes and honour an explicit `data-theme` over the system setting.

### Don't:
- **Don't** dress the interface in a physical-library or any other costume metaphor; the user rejected it.
- **Don't** reduce state to a row of equal-weight coloured pills.
- **Don't** use green, amber or red for anything but coverage, verdicts, job status, errors and destructive actions.
- **Don't** add a second accent colour or make blue a fill for primary buttons.
- **Don't** set headings, labels or prose in Red Hat Mono.
- **Don't** load fonts from a CDN; the families ship with the app.
- **Don't** give rows, cards or panels a shadow; only the open document page and floating messages lift.
