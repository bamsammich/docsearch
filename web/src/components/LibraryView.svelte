<script lang="ts">
  import type { Document } from "../gen/docsearch/document/v1/document_pb";
  import { SourceKind } from "../gen/docsearch/type/v1/common_pb";
  import { headline, verdictOrder } from "../lib/coverage";
  import { findingsOf } from "../lib/findings";
  import { coverageFor, library, verdictOf } from "../lib/library.svelte";
  import { queue } from "../lib/queue.svelte";
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

  $effect(() => {
    const mq = matchMedia("(max-width: 960px)");
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
      { n: `${counts.all - counts.attention} of ${counts.all}`, label: "fully searchable", show: true },
      { n: String(counts.degraded), label: "degraded", show: counts.degraded > 0 },
      { n: String(counts.unusable), label: "unusable", show: counts.unusable > 0 },
    ].filter((p) => p.show),
  );
  const visible = $derived([...(filter !== "good" ? problems : []), ...(filter !== "attention" ? healthy : [])]);
  // On a wide screen the first document is always open beside the list.
  const openId = $derived(router.docId || (narrow ? "" : (visible[0]?.docId ?? "")));
  const open = $derived(openId ? library.byId(openId) : undefined);
  const sheet = $derived(narrow && !!router.docId && !!open);

  function select(id: string) {
    router.go("library", id);
  }

  function closeSheet() {
    const id = router.docId;
    router.go("library");
    queueMicrotask(() => document.querySelector<HTMLElement>(`[data-doc="${CSS.escape(id)}"]`)?.focus());
  }

  function trap(event: KeyboardEvent) {
    if (!sheet) return;
    if (event.key === "Escape") {
      closeSheet();
      return;
    }
    if (event.key !== "Tab") return;
    const box = event.currentTarget as HTMLElement;
    const stops = [...box.querySelectorAll<HTMLElement>("button:not([disabled]), a[href], summary, [tabindex='0']")];
    const first = stops[0];
    const last = stops[stops.length - 1];
    if (!first || !last) return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  const reason = (d: Document) => {
    const report = library.reports[d.docId];
    if (!report) return "";
    const coverage = coverageFor(d, report);
    const lead = findingsOf({
      coverage,
      findings: report.findings,
      problems: report.problems,
      warnings: [],
      chunkCount: Number(report.measurements?.chunkCount ?? 0n),
      headlessChunks: Number(report.measurements?.headlessChunks ?? 0n),
    }).blocks[0]?.name;
    return `${lead ? `${lead}. ` : ""}${headline(coverage)}`;
  };

  const meta = (d: Document) =>
    [
      d.sourceKind === SourceKind.SITE ? "site" : d.format.toUpperCase(),
      d.pageCount !== undefined ? `${n.format(d.pageCount)} pp` : "",
      d.warnings.length ? (d.warnings.length === 1 ? "1 warning" : `${d.warnings.length} warnings`) : "",
    ]
      .filter(Boolean)
      .join(" · ");
</script>

{#if !library.loaded}
  <p class="muted">Loading the library…</p>
{:else if library.error}
  <div class="banner" role="alert"><span>{library.error}</span><button class="btn" onclick={() => library.load()}>Try again</button></div>
{:else if !library.docs.length}
  <EmptyLibrary />
{:else}
  <div class="library">
    <div>
      <p class="summary tnum">
        {#each summary as part, i (part.label)}{#if i}<span class="sep" aria-hidden="true">{" · "}</span>{/if}<span class="part"><b>{part.n}</b> {part.label}</span>{/each}
      </p>
      <div class="toolbar">
        <div class="filters" role="group" aria-label="Filter by verdict">
          <button aria-pressed={filter === "all"} onclick={() => (filter = "all")}>All <span class="n">{counts.all}</span></button>
          <button aria-pressed={filter === "attention"} onclick={() => (filter = "attention")}>Needs attention <span class="n">{counts.attention}</span></button>
          <button aria-pressed={filter === "good"} onclick={() => (filter = "good")}>Good <span class="n">{counts.all - counts.attention}</span></button>
        </div>
        <label class="find">
          <span class="sr-only">Filter documents</span><Icon name="search" />
          <input type="search" placeholder="Filter by title or source" bind:value={find} />
        </label>
      </div>
      {#if queue.active && !find}
        <button class="notice" onclick={() => router.go("ingest")}>
          <span>{queue.active === 1 ? "1 document is" : `${queue.active} documents are`} still ingesting and not yet searchable.</span><span>View</span>
        </button>
      {/if}
      {#if filter !== "good" && problems.length}
        <div class="group-label"><h2 class="label">Needs attention</h2></div>
        <ul class="problems">
          {#each problems as d (d.docId)}
            {@const report = library.reports[d.docId]}
            <li class="prow {verdicts[d.docId]}">
              <button data-doc={d.docId} aria-current={openId === d.docId ? "true" : undefined} onclick={() => select(d.docId)}>
                <span class="top"><span class="t">{d.title}</span><Badge verdict={verdicts[d.docId]!} /></span>
                {#if report}<CoverageStrip coverage={coverageFor(d, report)} thin />{/if}
                <span class="why">{reason(d) || "Measuring coverage…"}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
      {#if filter !== "attention" && healthy.length}
        <div class="group-label"><h2 class="label">Fully searchable</h2><span class="label tnum">{healthy.length}</span></div>
        <ul class="healthy">
          {#each healthy as d (d.docId)}
            <li class="hrow">
              <button data-doc={d.docId} aria-current={openId === d.docId ? "true" : undefined} onclick={() => select(d.docId)}>
                <span class="t">{d.title}</span>
                <span class="m">{meta(d)}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
      {#if !visible.length}<p class="ok-line">No title or source matches “{find}”.</p>{/if}
      <details class="legend">
        <summary><Icon name="chevron" />What the verdicts mean</summary>
        <dl>
          <dt><Badge verdict="good" /></dt><dd>Agents can search all of it and narrow by chapter.</dd>
          <dt><Badge verdict="degraded" /></dt><dd>Agents miss part of it: pages that never reached the index, or chapters they can't tell apart.</dd>
          <dt><Badge verdict="unusable" /></dt><dd>Agents get little or nothing from it.</dd>
        </dl>
      </details>
    </div>
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <div
      class="reading"
      class:sheet
      role={sheet ? "dialog" : undefined}
      aria-modal={sheet ? "true" : undefined}
      aria-labelledby={sheet ? "rec-title" : undefined}
      onkeydown={trap}
    >
      {#if open}
        {#key open.docId}<DocumentPanel doc={open} {sheet} onclose={closeSheet} />{/key}
      {:else if router.docId}
        <div class="panel"><p class="ok-line">No document “{router.docId}” here. It may have been removed.</p></div>
      {/if}
    </div>
  </div>
{/if}

