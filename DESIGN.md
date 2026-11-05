---
name: docsearch Library
description: A quiet dark frame around a library table that shows, document by document, how much an agent can actually reach.
colors:
  frame: "#14181d"
  frame-2: "#262c33"
  frame-3: "#343b43"
  frame-line: "#2c333b"
  on-frame: "#ffffff"
  on-frame-muted: "#c3cad2"
  frame-live: "#9bb8ff"
  frame-alert: "#ff9b91"
  bg: "#ffffff"
  surface: "#ffffff"
  surface-2: "#f3f4f6"
  line: "#e6e7eb"
  line-soft: "#f0f1f3"
  line-strong: "#d3d6dc"
  ink: "#16181d"
  ink-2: "#3a3f47"
  muted: "#5d636c"
  accent: "#3a5ccc"
  accent-weak: "#eef1fb"
  good: "#1d7442"
  good-bg: "#e4f2e9"
  good-fill: "#2f8f57"
  warn: "#855000"
  warn-bg: "#fcf4e0"
  warn-fill: "#e0a820"
  bad: "#b42318"
  bad-bg: "#fdecea"
  lost: "#bcc2ca"
  on-bad: "#ffffff"
  on-warn: "#1a1400"
  code-bg: "#14181d"
  code-fg: "#e4e6e8"
  frame-dark: "#0b0d10"
  frame-2-dark: "#1c2127"
  frame-3-dark: "#2a3038"
  frame-line-dark: "#20252b"
  bg-dark: "#15181c"
  surface-dark: "#1a1d22"
  surface-2-dark: "#22262c"
  line-dark: "#2b3037"
  line-soft-dark: "#23272d"
  line-strong-dark: "#3a4048"
  ink-dark: "#e8eaed"
  ink-2-dark: "#c8ccd2"
  muted-dark: "#a2a9b2"
  accent-dark: "#8ea8ff"
  accent-weak-dark: "#1e2742"
  good-dark: "#7fcf9d"
  good-bg-dark: "#15291d"
  good-fill-dark: "#4f9d6c"
  warn-dark: "#e8be62"
  warn-bg-dark: "#2b2410"
  warn-fill-dark: "#c99325"
  bad-dark: "#f4897d"
  bad-bg-dark: "#351b19"
  lost-dark: "#59616b"
  on-bad-dark: "#1a1d22"
typography:
  display:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "48px"
    fontWeight: 600
    lineHeight: 1
    letterSpacing: "-0.035em"
    fontFeature: "\"tnum\", \"cv11\", \"ss01\""
  headline:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "22px"
    fontWeight: 700
    lineHeight: 1.25
    letterSpacing: "-0.015em"
  title:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "20px"
    fontWeight: 600
    lineHeight: 1.5
    letterSpacing: "-0.01em"
  body:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.5
    fontFeature: "\"cv11\", \"ss01\""
  body-dense:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.5
  section-title:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "13px"
    fontWeight: 600
    lineHeight: 1.5
  label:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "12px"
    fontWeight: 600
    lineHeight: 1.5
  meta:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "12px"
    fontWeight: 400
    lineHeight: 1.5
  mono:
    fontFamily: "JetBrains Mono, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "0.92em"
    fontWeight: 400
  code:
    fontFamily: "JetBrains Mono, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "12px"
    fontWeight: 400
    lineHeight: 1.6
rounded:
  hair: "2px"
  bar: "3px"
  xs: "4px"
  sm: "6px"
  field: "7px"
  md: "8px"
  search: "9px"
  card: "10px"
  panel: "12px"
  pill: "14px"
spacing:
  hair: "2px"
  xs: "4px"
  sm: "6px"
  base: "8px"
  row: "10px"
  md: "12px"
  gutter: "14px"
  lg: "16px"
  page-y: "20px"
  page-x: "24px"
  section: "28px"
components:
  button:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: "0 12px"
    height: "34px"
  button-hover:
    backgroundColor: "{colors.surface-2}"
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.bg}"
    rounded: "{rounded.md}"
    padding: "0 12px"
    height: "34px"
  button-on-frame:
    backgroundColor: "{colors.on-frame}"
    textColor: "{colors.frame}"
    rounded: "{rounded.md}"
    padding: "0 12px"
    height: "34px"
  button-quiet:
    textColor: "{colors.ink-2}"
    rounded: "{rounded.md}"
    padding: "0 12px"
    height: "34px"
  button-quiet-hover:
    backgroundColor: "{colors.surface-2}"
    textColor: "{colors.ink}"
  button-danger-solid:
    backgroundColor: "{colors.bad}"
    textColor: "{colors.on-bad}"
    rounded: "{rounded.md}"
    padding: "0 12px"
    height: "34px"
  button-small:
    rounded: "{rounded.md}"
    padding: "0 10px"
    height: "30px"
  button-block:
    rounded: "{rounded.md}"
    width: "100%"
    height: "42px"
  input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    rounded: "{rounded.field}"
    padding: "0 10px"
    height: "34px"
  search-field:
    backgroundColor: "{colors.frame-2}"
    textColor: "{colors.on-frame}"
    typography: "{typography.body}"
    rounded: "{rounded.search}"
    padding: "0 6px 0 12px"
    height: "38px"
  search-scope:
    backgroundColor: "{colors.frame-3}"
    textColor: "{colors.on-frame}"
    rounded: "{rounded.sm}"
    padding: "0 6px"
    height: "26px"
  topbar:
    backgroundColor: "{colors.frame}"
    textColor: "{colors.on-frame}"
    padding: "10px 20px"
    height: "58px"
  side-nav:
    backgroundColor: "{colors.frame}"
    textColor: "{colors.on-frame-muted}"
    padding: "12px 10px"
    width: "216px"
  nav-item:
    textColor: "{colors.on-frame-muted}"
    rounded: "{rounded.field}"
    padding: "0 10px"
    height: "36px"
  nav-item-active:
    backgroundColor: "{colors.frame-2}"
    textColor: "{colors.on-frame}"
  badge-good:
    backgroundColor: "{colors.good-bg}"
    textColor: "{colors.good}"
    typography: "{typography.label}"
    rounded: "{rounded.sm}"
    padding: "2px 8px"
  badge-degraded:
    backgroundColor: "{colors.warn-bg}"
    textColor: "{colors.warn}"
    typography: "{typography.label}"
    rounded: "{rounded.sm}"
    padding: "2px 8px"
  badge-unusable:
    backgroundColor: "{colors.bad-bg}"
    textColor: "{colors.bad}"
    typography: "{typography.label}"
    rounded: "{rounded.sm}"
    padding: "2px 8px"
  filter-switch:
    backgroundColor: "{colors.surface-2}"
    rounded: "{rounded.md}"
    padding: "2px"
  filter-option:
    textColor: "{colors.muted}"
    typography: "{typography.body-dense}"
    rounded: "{rounded.sm}"
    padding: "0 10px"
    height: "28px"
  filter-option-active:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
  chip:
    backgroundColor: "{colors.surface}"
    typography: "{typography.body-dense}"
    rounded: "{rounded.pill}"
    padding: "0 11px"
    height: "28px"
  table-row:
    textColor: "{colors.ink}"
    typography: "{typography.body-dense}"
    padding: "9px 10px"
  table-row-selected:
    backgroundColor: "{colors.accent-weak}"
    rounded: "{rounded.md}"
  coverage-bar:
    backgroundColor: "{colors.surface-2}"
    rounded: "{rounded.bar}"
    height: "8px"
  coverage-bar-thin:
    backgroundColor: "{colors.surface-2}"
    rounded: "{rounded.hair}"
    height: "4px"
  check-degraded:
    backgroundColor: "{colors.warn-bg}"
    textColor: "{colors.ink-2}"
    typography: "{typography.body-dense}"
    padding: "12px 14px"
  check-unusable:
    backgroundColor: "{colors.bad-bg}"
    textColor: "{colors.ink-2}"
    typography: "{typography.body-dense}"
    padding: "12px 14px"
  card:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.card}"
    padding: "12px 16px"
  panel:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.panel}"
    padding: "18px 20px 20px"
  code-block:
    backgroundColor: "{colors.code-bg}"
    textColor: "{colors.code-fg}"
    typography: "{typography.code}"
    rounded: "{rounded.card}"
    padding: "14px 16px"
  toast:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.bg}"
    typography: "{typography.body-dense}"
    rounded: "{rounded.md}"
    padding: "9px 14px"
---

# Design System: docsearch Library

## Overview

**Creative North Star: "The Quiet Frame"**

A near-black frame (side navigation and top bar) holds a plain working area, and the working area holds one thing that matters: a table of documents and the selected document's check run beside it. The frame never changes between themes and never competes; it carries the brand mark, the two sections with their counts, and the search field, centred in the top bar, with Add document at its right. Everything a person reads sits in the working area, white in light and graphite in dark.

The system is dense and typographic: Inter at 13 to 14px, small radii, hairline dividers, almost no shadow. Colour is spent on state, not decoration. Healthy documents recede to lighter, tighter rows; degraded and unusable documents lead with heavier names, a reason line in their state colour, and a bar long enough to show its split. Coverage, the share of a document an agent can reach, is the one shared visual: a solid three-part bar, reachable by chapter, reachable with unreliable chapters, not indexed.

This world deliberately replaced an earlier one. Rejected and not to return: hatched or textured coverage bars, the previous typeface and looser density, and any dressing of the interface as a physical library (wood, paper, shelves, spines as ornament). The stack-of-books logo is the single sanctioned book reference; it is a mark, not a theme.

**Key Characteristics:**
- Dark frame (side navigation and top bar) around a light or graphite working area, in both themes.
- Search field centred in the top bar, reachable from anywhere with `/`.
- Library table with the selected document's check-run detail beside it; below 1180px the open document takes the table's place inside the frame.
- Verdicts are an icon plus a word, never colour alone.
- Coverage as solid green, amber and grey segments with the numbers beside them.
- One blue accent for focus, selection, links and live work.
- Inter for everything a person reads; JetBrains Mono only for machine strings.

## Colors

A neutral graphite-and-white palette with one blue accent and three state families (good, warn, bad), each in a text, tint and fill strength.

### Primary
- **Index Blue** (accent): focus rings, the selected table row's tint (accent-weak), links, search-term highlights, the running ingest phase and running job status. Nothing else.
- **Frame Blue** (frame-live): the accent's lighter cousin for use on the dark frame: the search field's focus border and halo, and the side navigation's live counts.

### Neutral
- **Frame Graphite** (frame): the side navigation and top bar; also the code block ground (code-bg). Deepens to frame-dark in dark theme but stays the darkest surface on screen.
- **Frame Raised** (frame-2): the search field, the active and hovered navigation item.
- **Frame Control** (frame-3): the scope select inside the search field, the avatar, the shortcut key's border.
- **Frame Seam** (frame-line): the search field's resting border and the navigation's edge.
- **Frame Text** (on-frame / on-frame-muted): white for active items and entered text; the muted grey for resting navigation and placeholders.
- **Working White** (bg / surface): the working area and every card in light theme; graphite (bg-dark, surface-dark) in dark.
- **Wash** (surface-2): hover rows, the filter switch track, the coverage bar track, disabled inputs.
- **Rules** (line, line-soft, line-strong): card borders and table header rule; row dividers; input and button borders and dashed affordances.
- **Ink** (ink, ink-2, muted): primary text and the primary button; secondary prose and healthy row names; meta, counts and hints.

### State
- **Searchable Green** (good, good-bg, good-fill): the good verdict's word and tint; the "reachable by chapter" coverage segment.
- **Unreliable Amber** (warn, warn-bg, warn-fill): the degraded verdict, degraded checks and reason lines; the "chapters unreliable" segment; the instance-label strip (warn-fill with on-warn text).
- **Unusable Red** (bad, bad-bg, on-bad): the unusable verdict and checks, errors, failed jobs, the destructive button and its confirmation; the frame's alert count uses frame-alert.
- **Not Indexed Grey** (lost): the "not indexed" coverage segment. Lost content is neutral, not alarming; the verdict carries the alarm.

### Named Rules
**The Dark Frame Rule.** The side navigation and top bar are always frame graphite, in light and dark theme alike. Controls on the frame use the frame-* and on-frame tokens, never working-area tokens.

**The One Blue Rule.** The accent marks focus, selection, links and live work only. A second hue for emphasis is not part of this system.

**The Icon-and-Word Rule.** Every verdict and job status is an icon plus a word. Colour reinforces; it never carries state alone.

**The Solid Segment Rule.** Coverage segments are flat fills separated by a 2px gap (1px on the thin bar). Never hatched, striped, patterned or textured.

## Typography

**Display Font:** Inter (with system-ui, -apple-system, Segoe UI, sans-serif)
**Body Font:** Inter
**Label/Mono Font:** JetBrains Mono (with ui-monospace, SF Mono, Menlo, monospace)

**Character:** One neutral sans for everything a person reads, set small and tight, with Inter's alternate digits and single-storey a (cv11, ss01). The mono face appears only where the string is the machine's: file names, IDs, page ranges, hosts, URLs, chunk IDs and raw MCP output. Both faces are self-hosted.

### Hierarchy
- **Display** (600, 48px, line-height 1, -0.035em, tabular): the reach figure, the count of units reachable by chapter, in the document detail. 40px on phones. The only large type in the UI.
- **Headline** (700, 22px, 1.25, -0.015em, balanced wrap): the selected document's title, the reader's title, sign-in heading (600). 20px on phones.
- **Title** (600, 20px, -0.01em): page titles (Library, Ingest, Search, empty states).
- **Body** (400, 14px, 1.5): default text, buttons, inputs, passage text (line-height 1.6). Prose capped at 72ch.
- **Body Dense** (400, 13px): the library table, checks, hits, notes, outline, summaries.
- **Section Title** (600, 13px): headings inside the detail and form labels; a lighter muted count may follow.
- **Label** (600, 12px): verdict badges, job status, navigation counts. Sentence case, no tracking.
- **Meta** (400, 12px, muted): source lines, hit locations, hints, table header.

### Named Rules
**The Two Faces Rule.** Inter for reading, JetBrains Mono for machine strings. Mono never sets prose, headings or labels.

**The Tabular Count Rule.** Every number that can be compared (counts, reach figures, ranks, phase values, the coverage key) is tabular.

**The Weight Leads Rule.** Problem rows set their names at 600; healthy rows drop to 400 in ink-2. Weight, not size, ranks the table.

## Layout

A two-column app grid: a 216px sticky side navigation spanning full height, and a content column whose first row is the 58px sticky top bar. The top bar is a three-column grid (minmax(150px, 1fr), up to 600px, minmax(150px, 1fr)) so the search field stays centred and shrinks before anything overlaps it. At 1180px and below the bar drops to two columns and the field fills it up to Add document. Views pad 20px by 24px with 64px at the foot.

The library view breaks out of that padding into its own grid: the table (minmax(0, 1fr)) and the detail column (400 to 520px), the detail sticky under the top bar, scrolling on its own, separated by a single line. Table rows are a three-column grid: name (with kind and reason line), status (104px), and reach (minmax(128px, 30%)), 14px apart.

Search results run a reading column up to 860px beside a 300px sticky sources card, 28px apart. The reader puts a 240px sticky contents list beside passages capped at 72ch. Ingest sets a 360px form panel beside the job list.

Spacing steps are 2, 4, 6, 8, 10, 12, 14, 16, 20, 24 and 28px; most internal gaps are 8 to 14px, section gaps in the detail are 22px.

Responsive behaviour:
- At 1180px and below the detail panel leaves the grid: an open document takes the table's place inside the frame (content capped at 760px), with a Library button and Escape to go back. The side navigation and top bar never leave. Search sources and the ingest form stack.
- At 1023px and below the reader's contents fold into a bordered disclosure above the text.
- At 760px and below the side navigation becomes a fixed bottom tab bar: icon over label (50px tall), each count a 16px badge on the icon's top-right corner (frame-live, or frame-alert when something needs attention), the current tab marked by colour alone, the brand moves into the top bar, the search field takes its own row (top bar 104px), the shortcut key hides, Add document shrinks to its icon, and table rows put the reach bar on its own line.

### Named Rules
**The One Scale Rule.** Every coverage bar in the library table is drawn on one size scale, chunk count, as a share of the longest document (never shorter than 4%), with a note stating the longest. Degraded and unusable rows keep a 48px minimum bar so their split stays visible.

**The Centred Search Rule.** The search field is centred in the top bar on every page wide enough to centre it, with its scope select inside it, and `/` focuses it from anywhere outside a text field.

## Elevation & Depth

The working area is flat. Depth comes from tone (the dark frame against the light area, wash on hover, tinted check rows) and from 1px rules. Cards, the table, the detail and the panels carry borders, not shadows. A shadow appears only on things that float above the page: the toast, the account popover and the sign-in card. The filter switch's active option gets a 1px lift so it reads as pressed-out of its track. Focus uses a 2px accent outline at 2px offset; text fields use a 3px soft halo.

### Shadow Vocabulary
- **Float** (`box-shadow: 0 1px 2px rgba(20, 24, 28, .06), 0 8px 24px rgba(20, 24, 28, .08)`; dark `0 1px 2px rgba(0,0,0,.4), 0 8px 24px rgba(0,0,0,.3)`): toast, account popover, sign-in card.
- **Raised Option** (`box-shadow: 0 1px 2px rgba(0,0,0,.08)`): the active option in a filter or segmented switch.
- **Field Halo** (`box-shadow: 0 0 0 3px var(--accent-weak)`): focused inputs in the working area.
- **Frame Halo** (`box-shadow: 0 0 0 3px rgba(155, 184, 255, .22)`): the focused search field on the frame.
- **Target Ring** (`box-shadow: 0 0 0 1px var(--accent)`): the passage a link landed on in the reader.

### Named Rules
**The Flat Working Area Rule.** If it sits in the page, it gets a border; if it floats over the page, it gets the float shadow. Nothing else casts one.

## Shapes

Small, consistent corners on rectangular forms. Radii step from 2px (thin bar, key swatches, highlights) and 3px (coverage bar) through 4px (focus outline, shortcut key), 6px (badges, switch options, scope select), 7px (inputs, navigation items), 8px (buttons, the selected row, notices, alerts), 9px (search field), 10px (cards: checks, hits, notes, sources, passages), to 12px (panels, sign-in card). Chips are the one pill (14px). The avatar is the one circle.

Borders are 1px. Dashed rules mark affordances and gaps, not state: the hidden-documents notice, the file drop zone (1.5px), a reader gap, and the search cutoff line. Icons are one stroke set drawn on a 24-unit grid at 1.7 stroke (2.1 to 2.2 inside badges, checks and statuses), round caps and joins, 16px by default.

The brand mark is a stack of three offset books, spine out, each with a band cut near its end. It is drawn in the text colour with the band cut in the ground colour, so it reads on the frame and on the working area; the favicon is the same stack in white on a frame-graphite rounded square.

## Components

### Buttons
Compact and plain; the primary inverts the ink.
- **Shape:** gently rounded (8px), 34px tall, 12px side padding, 14px weight 500, icon and label 7px apart.
- **Default:** surface ground, line-strong border, ink text; hover washes to surface-2.
- **Primary:** ink ground and border with bg-coloured text (so it is near-black on light and near-white on dark); hover drops to 88% opacity.
- **On Frame:** white with frame text, weight 600, for Add document in the top bar.
- **Quiet:** no border or ground, ink-2 text; hover washes. Used for secondary actions in the detail's foot and back buttons.
- **Danger / Danger Solid:** bad-coloured text on default, or a bad ground with on-bad text inside the delete confirmation.
- **Small** (30px, 13px) and **Block** (full width, 42px, 15px, centred) variants. Disabled is 50% opacity.
- **Link Button:** accent text, underlined at a 3px offset, no chrome.

### Verdict Badge
An icon and a word: check and "Good", warning triangle and "Degraded", stop circle and "Unusable". 12px weight 600, 13px icon, 6px radius, tinted ground (good-bg, warn-bg, bad-bg) with matching text. In the library table's status column it is plain (no tint, no padding) so a column of healthy rows does not weigh as much as a broken one; on the document it is tinted. Job statuses follow the same pattern (running and cancelling in accent, queued and cancelled in wash, stalled in warn, failed in bad, done in good).

### Coverage Bar
The signature component. A flex row of up to three solid segments, each sized by its count: reachable by chapter (good-fill), reachable with unreliable chapters (warn-fill), not indexed (lost), on a surface-2 track, 8px tall with a 3px radius and 2px gaps; the thin variant is 4px, 2px radius, 1px gaps. Each segment keeps a 3px minimum. It is an image with a full sentence as its label. In the detail it sits under the reach figure with a key of 9px swatches and tabular counts, and opening a document grows it from the left (0.6s, cubic-bezier(.16, 1, .3, 1)) while the key settles in; with no fill mode, the bar rests whole wherever the animation does not run.

### Library Table
A grid list, not a table of cards. Header row in 12px muted meta over a line; rows 9px by 10px with a line-soft divider, hover wash, and the selected row tinted accent-weak with an 8px radius and no divider. Healthy rows tighten to 7px block padding with 400-weight ink-2 names. Problem rows carry a 12px reason line in warn or bad. The reach cell stacks the tabular count over the thin coverage bar. Above the table, a filter switch (surface-2 track, 2px padding, 28px options, the active one on surface with the raised-option shadow) and a 30px title filter with an inset search icon. When the table's own column is 560px or narrower (a wide panel beside it, or a phone), the header hides and each row takes two lines: name and verdict, then the reach cell.

### Document Panel
Beside the table above 1180px, 480px wide by default. A 7px grip on its left edge resizes it by drag or by arrow keys (Home and End for narrowest and widest), shows a 3px accent line on hover, focus or drag, and the width is remembered in the browser. The panel never takes more than leaves the table 360px. Two 30px quiet icon buttons at its top right expand it to that widest and collapse it back, and close it (Escape does too), giving the table the whole width until a row is selected again.

### Check Run
The selected document's checks are one bordered list (10px radius, rows separated by lines). Each row is a 16px icon column and the body: a 600-weight check name, its page range as a small mono pill (11px, 5px radius, bordered), what agents lose, and a remedy line with actions. Degraded rows tint warn-bg with warn icon and name; unusable rows tint bad-bg with bad icon and name; passing rows are untinted ink-2 with a good check icon.

### Cards / Containers
- **Corner Style:** 10px for cards (hits, notes, passages, sources, jobs), 12px for panels and the sign-in card.
- **Background:** surface, with warn-bg for a warning note, bad-bg for banners and the delete confirmation.
- **Shadow Strategy:** none; see the Flat Working Area Rule.
- **Border:** 1px line; a failed job's border turns bad; a targeted passage gets the accent target ring; a passage with no heading path gets a warn-fill border.
- **Internal Padding:** 12 to 16px for cards, 18 to 20px for panels, 28px for the sign-in card.

### Inputs / Fields
- **Style:** 34px, surface ground, 1px line-strong border, 7px radius, 10px side padding, 14px text; placeholders in muted at full opacity.
- **Focus:** border turns accent with a 3px accent-weak halo (the field halo).
- **Error / Disabled:** errors are 13px bad text under the field; disabled fields wash to surface-2. The password field is 44px with an inline reveal button.

### Search Field
Lives on the frame: frame-2 ground, frame-line border, 9px radius, 38px tall, a search icon, the input, a scope select in a frame-3 chip ("All documents" or one document), and a `/` key hint. Focus turns the border frame-live with the frame halo.

### Navigation
The side navigation is frame graphite with the brand at top, sections in the middle, and the account button at the foot. Sections are 36px, 7px radius, 14px weight 500 in on-frame-muted with a 16px icon; hover and the current page take frame-2 and on-frame. A section may carry a tabular 12px count at its right: frame-live for running work, frame-alert for documents or jobs that need attention. On phones it becomes the fixed bottom bar described in Layout. The account popover opens above the account button as a 270px floating card.

### Toast and Instance Strip
The toast is an inverted (ink ground, bg text) 8px-radius box of 13px text centred at the bottom, with the float shadow. When the operator names an instance, a warn-fill strip with on-warn text spans the top of every page so a test server is never taken for the real library.

## Do's and Don'ts

### Do:
- **Do** keep the side navigation and top bar on frame graphite (#14181d light, #0b0d10 dark) in both themes, with the search field centred in the top bar.
- **Do** pair every verdict and status with its icon and its word; use the plain badge in table columns and the tinted badge on a document.
- **Do** draw coverage as solid good-fill, warn-fill and lost segments with the numbers alongside, and draw every library-table bar on the one chunk-count scale with a 48px floor for problem rows.
- **Do** let healthy rows recede (400 weight, ink-2, 7px padding) and let problem rows lead (600 weight, a reason line in their state colour).
- **Do** reserve the accent for focus, selection, links, search highlights and live work.
- **Do** set file names, IDs, page ranges, hosts, URLs and raw MCP output in JetBrains Mono, and everything else in Inter.
- **Do** use tabular numerals for every comparable number.
- **Do** honour reduced motion: all animation and transition stops; the coverage bar rests whole.

### Don't:
- **Don't** hatch, stripe, texture or pattern a coverage bar or any fill.
- **Don't** dress the interface as a physical library: no wood, paper, shelves, card-catalogue or spine ornament. The book-stack logo is the one book reference, and it stays a mark.
- **Don't** carry state by colour alone.
- **Don't** return to the previous typeface or the looser density this system replaced; stay on Inter at 13 to 14px.
- **Don't** give in-page cards, tables or the detail panel a shadow; borders and tone carry depth.
- **Don't** add a second accent hue, or use the accent for decoration.
- **Don't** set prose, headings or labels in the mono face.
- **Don't** size table bars on per-document or mixed scales; one scale, chunk count.
