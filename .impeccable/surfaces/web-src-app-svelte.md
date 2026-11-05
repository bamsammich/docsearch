---
version: 1
slug: "web-src-app-svelte"
primary_target: "web/src/App.svelte"
related_targets: ["web/src/app.css"]
---

# Library app shell

Scope: the whole embedded web UI (library, document detail, search results, ingest, reader, sign-in). Mode: Operate.

Audience and job: a person running coding agents opens the UI with a task, usually after queueing a document or after an agent's search came back thin. They need to know within seconds which documents are searchable, which are degraded or unusable, and why, then search or act on one.

Confirmed with the user (2026-11-05): library-first; drop the hatched coverage bars; change type and density; keep loud verdicts, coverage numbers, both themes, and the exact MCP view in search. The user chose canvas board E: D's layout, A's detail panel, C's search bar, with a dark sidenav and top bar and the search box centred in the top bar.

## Direction contract

THESIS: A quiet app frame around a library table, where selecting a document opens a check-run detail that pins each problem to its pages. Refuses the equal-weight pill dashboard: verdicts are an icon and a word, and healthy rows recede.

OWN-WORLD: Near-black frame (#14181d sidenav and top bar, #262c33 search field) around a white working area; Inter for everything a person reads, JetBrains Mono only for file names, IDs and page ranges; solid green, amber and neutral-grey coverage segments, never hatched; one blue accent for focus, selection, links and live work; ink-black primary buttons on light, white on dark.

STORY: The person sees how many documents are searchable, spots the problems at the top of the table, opens one, reads which pages fail and what agents lose, then searches it, reads it, opens the original, or re-ingests.

FIRST VIEWPORT: Dark sidenav at left (brand, Library with attention count, Ingest with running count). Dark top bar with the search field centred and Add document at the right. Below, the library table (filter switch, title filter, Name, Status, Reachable by chapter on one shared page scale) and, when a row is selected, the detail panel at right: title, verdict, actions under the title, the reach figure and bar, checks, outline, secondary actions.

FORM: Canon layout fused with the check-run detail (assigned, position 4) and the dense-modules search bar; seed key 5d63e3e8.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

Signature interaction: selecting a row swaps the detail panel in place without a page change; the search field is reachable from anywhere with `/`.
