<script lang="ts">
  import type { Document } from "../gen/docsearch/document/v1/document_pb";
  import { SourceKind } from "../gen/docsearch/type/v1/common_pb";
  import { total, verdictOrder } from "../lib/coverage";
  import { findingsOf } from "../lib/findings";
  import { coverageFor, library, verdictOf } from "../lib/library.svelte";
  import { queue } from "../lib/queue.svelte";
  import { minPanel, minTable, panel } from "../lib/panel.svelte";
  import { router } from "../lib/router.svelte";
  import Badge from "./Badge.svelte";
  import CoverageStrip from "./CoverageStrip.svelte";
  import DocumentPanel from "./DocumentPanel.svelte";
  import EmptyLibrary from "./EmptyLibrary.svelte";
  import Icon from "./Icon.svelte";

  type Filter = "all" | "attention" | "good";
  let filter = $state<Filter>("all");
  let find = $state("");
  let narrow = $state(false);

  // Below this width the table takes the page and a document opens as a sheet.
  $effect(() => {
    const mq = matchMedia("(max-width: 1180px)");
    narrow = mq.matches;
    const update = () => (narrow = mq.matches);
    mq.addEventListener("change", update);
    return () => mq.removeEventListener("change", update);
  });

  const n = new Intl.NumberFormat("en-US");
  const verdicts = $derived(Object.fromEntries(library.docs.map((d) => [d.docId, verdictOf(d, library.reports[d.docId])])));
  const matches = (d: Document) => {
    const q = find.trim().toLowerCase();
    return !q || d.title.toLowerCase().includes(q) || d.source.toLowerCase().includes(q);
  };
  const problems = $derived(
    library.docs
      .filter((d) => library.needsAttention(d) && matches(d))
      .sort((a, b) => verdictOrder[verdicts[a.docId]!] - verdictOrder[verdicts[b.docId]!] || a.title.localeCompare(b.title)),
  );
  const healthy = $derived(
    library.docs.filter((d) => !library.needsAttention(d) && matches(d)).sort((a, b) => a.title.localeCompare(b.title)),
  );
  const counts = $derived({
    all: library.docs.length,
    attention: library.docs.filter((d) => library.needsAttention(d)).length,
    degraded: library.docs.filter((d) => verdicts[d.docId] === "degraded").length,
    unusable: library.docs.filter((d) => verdicts[d.docId] === "unusable").length,
  });
  const summary = $derived(
    [
      { n: `${counts.all - counts.attention} of ${counts.all}`, label: "searchable", show: true },
      { n: String(counts.degraded), label: "degraded", show: counts.degraded > 0 },
      { n: String(counts.unusable), label: "unusable", show: counts.unusable > 0 },
    ].filter((p) => p.show),
  );
  const visible = $derived([...(filter !== "good" ? problems : []), ...(filter !== "attention" ? healthy : [])]);
  // On a wide screen the first document opens beside the table until the
  // panel is closed; selecting a row opens it again.
  const openId = $derived(router.docId || (narrow || panel.closed ? "" : (visible[0]?.docId ?? "")));
  const open = $derived(openId ? library.byId(openId) : undefined);
  const sheet = $derived(narrow && !!router.docId && !!open);

  const coverages = $derived(
    Object.fromEntries(
      library.docs.flatMap((d) => {
        const report = library.reports[d.docId];
        return report ? [[d.docId, coverageFor(d, report)] as const] : [];
      }),
    ),
  );
  // Every bar is drawn on one scale, chunks, which every document has whether
  // or not it has pages: a bar's length shows how big a document is beside
  // the others, and its segments show what agents lose.
  const sizes = $derived(
    Object.fromEntries(
      library.docs.map((d) => [d.docId, Number(library.reports[d.docId]?.measurements?.chunkCount ?? d.chunkCount ?? 0n)]),
    ),
  );
  const longest = $derived(Math.max(0, ...Object.values(sizes)));
  const scale = (id: string) => (longest ? `${Math.max(4, ((sizes[id] ?? 0) / longest) * 100)}%` : "0");

  function select(id: string) {
    panel.closed = false;
    router.go("library", id);
  }

  // The panel beside the table: dragged or arrowed to a width, expanded to
  // the widest the table allows, or closed so the table takes the page.
  let box: HTMLDivElement | undefined = $state();
  let drag = $state<{ x: number; width: number } | null>(null);
  const room = () => box?.clientWidth ?? innerWidth;

  function closePanel() {
    const id = openId;
    panel.closed = true;
    router.go("library");
    queueMicrotask(() => document.querySelector<HTMLElement>(`[data-doc="${CSS.escape(id)}"]`)?.focus());
  }

  function startDrag(event: PointerEvent) {
    drag = { x: event.clientX, width: panel.width };
    (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
  }

  function moveDrag(event: PointerEvent) {
    if (drag) panel.resize(drag.width + (drag.x - event.clientX), room());
  }

  function endDrag() {
    if (!drag) return;
    drag = null;
    panel.save();
  }

  const steps: Record<string, (w: number) => number> = {
    ArrowLeft: (w) => w + 24,
    ArrowRight: (w) => w - 24,
    Home: () => minPanel,
    End: () => room(),
  };

  function nudge(event: KeyboardEvent) {
    const step = steps[event.key];
    if (!step) return;
    event.preventDefault();
    panel.resize(step(panel.width), room());
    panel.save();
  }

  function closeSheet() {
    const id = router.docId;
    router.go("library");
    queueMicrotask(() => document.querySelector<HTMLElement>(`[data-doc="${CSS.escape(id)}"]`)?.focus());
  }

  // On a narrow screen the open document takes the table's place inside the
  // frame, so it starts at the top, and Escape goes back to the table.
  $effect(() => {
    if (sheet) scrollTo({ top: 0 });
  });

  function back(event: KeyboardEvent) {
    if (event.key !== "Escape") return;
    if (sheet) closeSheet();
    else if (!narrow) closePanel();
  }

  /** The first problem a document has, for its row in the table. */
  const reason = (d: Document) => {
    const report = library.reports[d.docId];
    const coverage = coverages[d.docId];
    if (!report || !coverage) return "";
    return (
      findingsOf({
        coverage,
        findings: report.findings,
        problems: report.problems,
        warnings: [],
        chunkCount: Number(report.measurements?.chunkCount ?? 0n),
        headlessChunks: Number(report.measurements?.headlessChunks ?? 0n),
      }).blocks[0]?.name ?? ""
    );
  };

  const formats: Record<string, string> = { pdf: "PDF", docx: "Word", html: "HTML", md: "Markdown", markdown: "Markdown", text: "Text", txt: "Text" };
  const kind = (d: Document) =>
    d.sourceKind === SourceKind.SITE ? "site" : (formats[d.format.toLowerCase()] ?? d.format.toUpperCase());
</script>

{#if !library.loaded}
  <p class="muted pad">Loading the library…</p>
{:else if library.error}
  <div class="banner" role="alert"><span>{library.error}</span><button class="btn" onclick={() => library.load()}>Try again</button></div>
{:else if !library.docs.length}
  <EmptyLibrary />
{:else}
  <div class="library" class:sheet-open={sheet} class:no-panel={!open && !narrow} class:dragging={!!drag}
    bind:this={box} style:--panel-w={`${panel.width}px`}>
    <section class="lib-list" aria-labelledby="lib-title">
      <div class="lib-head">
        <h1 id="lib-title">Library</h1>
        <p class="summary tnum">
          {#each summary as part, i (part.label)}{#if i}<span class="sep" aria-hidden="true">{" · "}</span>{/if}<span class="part"><b>{part.n}</b> {part.label}</span>{/each}
        </p>
      </div>
      <div class="toolbar">
        <div class="filters" role="group" aria-label="Show">
          <button aria-pressed={filter === "all"} onclick={() => (filter = "all")}>All <span class="n">{counts.all}</span></button>
          <button aria-pressed={filter === "attention"} onclick={() => (filter = "attention")}>Needs attention <span class="n">{counts.attention}</span></button>
          <button aria-pressed={filter === "good"} onclick={() => (filter = "good")}>Searchable <span class="n">{counts.all - counts.attention}</span></button>
        </div>
        <label class="find">
          <span class="sr-only">Filter documents by title or source</span><Icon name="filter" />
          <input type="search" placeholder="Filter by title" bind:value={find} />
        </label>
      </div>
      {#if queue.active && !find}
        <button class="notice" onclick={() => router.go("ingest")}>
          <span>{queue.active === 1 ? "1 document is" : `${queue.active} documents are`} still ingesting and not yet searchable.</span><span class="notice-go">View</span>
        </button>
      {/if}

      {#if visible.length}
        <div class="ltable">
          <div class="lhead" aria-hidden="true"><span>Name</span><span>Status</span><span>Reachable by chapter</span></div>
          <ul>
            {#each visible as d (d.docId)}
              {@const verdict = verdicts[d.docId]!}
              {@const coverage = coverages[d.docId]}
              {@const why = verdict === "good" ? "" : reason(d)}
              <li>
                <button class="lrow {verdict}" data-doc={d.docId} aria-current={openId === d.docId ? "true" : undefined} onclick={() => select(d.docId)}>
                  <span class="l-name">
                    <span class="t">{d.title}</span> <span class="k">· {kind(d)}</span>
                    {#if why}<span class="why">{why}</span>{/if}
                  </span>
                  <span class="l-status"><Badge {verdict} plain /></span>
                  <span class="l-reach tnum">
                    {#if coverage}
                      <span class="l-num">{n.format(coverage.byChapter)} / {n.format(total(coverage))} {coverage.unit}</span>
                      <span class="l-bar" style:width={scale(d.docId)}><CoverageStrip {coverage} thin /></span>
                    {:else if library.reportErrors[d.docId]}
                      <span class="l-num err">Couldn't check</span>
                    {:else}
                      <span class="l-num muted">Checking…</span>
                    {/if}
                  </span>
                </button>
              </li>
            {/each}
          </ul>
        </div>
        {#if longest}<p class="hint scale-note tnum">Bar length shows size in chunks: the longest has {n.format(longest)}.</p>{/if}
      {:else}
        <p class="ok-line">{find ? `No title or source matches “${find}”.` : "Nothing to show under this filter."}</p>
      {/if}
      <details class="legend">
        <summary><Icon name="chevron" />What the verdicts mean</summary>
        <dl>
          <dt><Badge verdict="good" /></dt><dd>Agents can search all of it and narrow by chapter.</dd>
          <dt><Badge verdict="degraded" /></dt><dd>Agents miss part of it: pages that never reached the index, or chapters they can't tell apart.</dd>
          <dt><Badge verdict="unusable" /></dt><dd>Agents get little or nothing from it.</dd>
        </dl>
      </details>
    </section>
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="reading" class:sheet onkeydown={back}>
      {#if open && !narrow}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
        <div
          class="resizer"
          class:dragging={!!drag}
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize the document panel"
          aria-valuenow={panel.width}
          aria-valuemin={minPanel}
          aria-valuemax={Math.max(minPanel, room() - minTable)}
          tabindex="0"
          onpointerdown={startDrag}
          onpointermove={moveDrag}
          onpointerup={endDrag}
          onpointercancel={endDrag}
          onkeydown={nudge}
        ></div>
      {/if}
      <div class="reading-scroll">
        {#if open}
          {#key open.docId}
            <DocumentPanel doc={open} {sheet} onclose={sheet ? closeSheet : closePanel}
              expanded={panel.expanded} onexpand={() => panel.toggle(room())} />
          {/key}
        {:else if router.docId}
          <p class="ok-line pad">No document “{router.docId}” here. It may have been removed.</p>
        {/if}
      </div>
    </div>
  </div>
{/if}
