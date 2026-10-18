---
version: 1
slug: "impeccable-mocks-library-index-html"
primary_target: ".impeccable/mocks/library/index.html"
related_targets: []
---

# Library UI (clickable demo)

Scope: clickable demo of the embedded library UI, synthetic data, before the Svelte build is approved. Visitor mode: Operate.

Audience and job: an account holder checks which documents their agents can actually search, adds documents and watches ingest, and previews MCP search exactly as an agent sees it. Auth is the MCP's bearer (oidc PKCE, token per tab, none on loopback); no sessions.

History: a card-catalog world (oak, brass, typewriter, stamps) was built first and rejected by the user as kitschy. A stripped version followed; impeccable critique scored it 25/40 and the user chose a coverage signature, all five fixes, Library as home.

Unresolved: upload depends on the uploads phase; refresh RPC; `source_path` and `ingested_at` on the Document API; coverage figures (reachable by chapter / chapters unreliable / not indexed) need a server-side source, likely from verify.

## Direction contract

THESIS: Coverage is the product's signature. Every document shows how much of it an agent can reach, as a three-part strip: by chapter, chapters unreliable, not indexed. Refuses both the costume of a library metaphor and the anonymous dev-console of equal-weight pills.

OWN-WORLD: Quiet neutral ground (cool off-white light, near-black dark), white surfaces, one blue accent for focus and live state, green / hatched amber / dusty red reserved for coverage and verdicts. Libre Franklin throughout, Red Hat Mono only for IDs, sources and URLs. Brand mark is three coverage bars.

STORY: The reader sees "9 of 12 fully searchable", reads the problem rows that lead with their strip and the finding, opens one to see what agents lose and the remedy button, and leaves the healthy documents collapsed.

FIRST VIEWPORT: Tabs (Library, Ingest, Search preview) with problem counts; Add document top right. Left column 340 to 440px: summary line, filters, problem rows with strips, then a dense "Fully searchable" list. Right: the open document with title, verdict, consequence headline, full-width coverage strip and key, findings with remedies.

FORM: Coverage strip, refined from the card-catalog pick (rank 1 of 7), seed key 81da5b7a. Signature interaction: selecting a problem row opens its document, whose strip expands to a keyed full-width bar.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance
