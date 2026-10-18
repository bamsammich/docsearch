<script lang="ts">
  import { tick } from "svelte";
  import type { ReadChunk } from "../gen/docsearch/document/v1/document_pb";
  import { documents, problem } from "../lib/api";
  import { library, verdictOf } from "../lib/library.svelte";
  import { callTool } from "../lib/mcp";
  import { readerHref, router } from "../lib/router.svelte";
  import Badge from "./Badge.svelte";
  import Icon from "./Icon.svelte";
  import OriginalButton from "./OriginalButton.svelte";

  interface Entry {
    heading: string;
    depth: number;
    chunk_id?: number;
    page_start?: number;
  }

  const pageSize = 40;
  const n = new Intl.NumberFormat("en-US");

  let chunks = $state<ReadChunk[]>([]);
  let total = $state(0);
  let error = $state("");
  let loading = $state(false);
  let entries = $state<Entry[]>([]);
  // Contents start open beside the text, and folded above it on a phone.
  let tocOpen = $state(matchMedia("(min-width: 961px)").matches);

  const docId = $derived(router.docId);
  const target = $derived(router.chunkId);
  const doc = $derived(library.byId(docId));
  const report = $derived(library.reports[docId]);
  const first = $derived(chunks.length ? Number(chunks[0]!.ordinal) : 0);
  const last = $derived(chunks.length ? Number(chunks[chunks.length - 1]!.ordinal) : -1);
  const uncovered = $derived((report?.measurements?.uncoveredPages ?? []).map(Number));

  // A new document, or a new chunk to open at, starts the run again.
  $effect(() => {
    const id = docId;
    const at = target;
    chunks = [];
    error = "";
    if (!id) return;
    void open(id, at);
  });

  $effect(() => {
    const id = docId;
    entries = [];
    if (!id) return;
    if (!library.reports[id]) void library.verify(id);
    void callTool<{ entries: Entry[] | null }>("outline", { doc_id: id, depth: 2 })
      .then((out) => {
        if (docId === id) entries = out.entries ?? [];
      })
      .catch(() => undefined);
  });

  async function open(id: string, at: number | null) {
    loading = true;
    try {
      const out = await documents.read({ docId: id, limit: BigInt(pageSize), ...(at !== null ? { atChunkId: BigInt(at) } : {}) });
      if (docId !== id) return;
      // Opened at a chunk, the run starts a few chunks before it, so the
      // target reads in its place.
      let run = out.chunks;
      const from = Number(out.fromOrdinal);
      if (at !== null && from > 0) {
        const lead = await documents.read({ docId: id, fromOrdinal: BigInt(Math.max(0, from - 3)), limit: BigInt(Math.min(3, from)) });
        run = [...lead.chunks, ...run];
      }
      chunks = run;
      total = Number(out.chunkCount);
      if (at !== null) {
        await tick();
        const el = document.getElementById(`chunk-${at}`);
        el?.scrollIntoView({ block: "start" });
        el?.focus({ preventScroll: true });
      }
    } catch (err) {
      error = problem(err);
    } finally {
      loading = false;
    }
  }

  async function more() {
    loading = true;
    try {
      const out = await documents.read({ docId, fromOrdinal: BigInt(last + 1), limit: BigInt(pageSize) });
      chunks = [...chunks, ...out.chunks];
    } catch (err) {
      error = problem(err);
    } finally {
      loading = false;
    }
  }

  async function earlier() {
    loading = true;
    const anchor = chunks[0] ? document.getElementById(`chunk-${chunks[0].chunkId}`) : null;
    const before = anchor?.getBoundingClientRect().top ?? 0;
    try {
      const from = Math.max(0, first - pageSize);
      const out = await documents.read({ docId, fromOrdinal: BigInt(from), limit: BigInt(first - from) });
      chunks = [...out.chunks, ...chunks];
      // Keep the chunk that was first where it was on screen.
      await tick();
      if (anchor) scrollBy(0, anchor.getBoundingClientRect().top - before);
    } catch (err) {
      error = problem(err);
    } finally {
      loading = false;
    }
  }

  /** Pages that never reached the index, between one chunk's page and the next's. */
  function gapBefore(i: number): number[] {
    const here = chunks[i]?.pageStart;
    // The document's first chunk counts from before page 1.
    const prev = i === 0 && first === 0 ? 0n : chunks[i - 1]?.pageStart;
    if (here === undefined || prev === undefined) return [];
    return uncovered.filter((p) => p > Number(prev) && p < Number(here));
  }

  /** Pages past the last chunk that never reached the index. */
  const tail = $derived.by(() => {
    const end = chunks[chunks.length - 1]?.pageStart;
    if (end === undefined || last + 1 < total) return [];
    return uncovered.filter((p) => p > Number(end));
  });

  function lost(list: number[]): string {
    return list.length === 1
      ? `Page ${list[0]} never reached the index. No search returns its text.`
      : `Pages ${ranges(list)} never reached the index. No search returns their text.`;
  }

  /** Sorted page numbers as runs: 4–6, 9. */
  function ranges(list: number[]): string {
    const runs: string[] = [];
    for (let i = 0; i < list.length; ) {
      let j = i;
      while (j + 1 < list.length && list[j + 1] === list[j]! + 1) j++;
      runs.push(j > i ? `${list[i]}–${list[j]}` : `${list[i]}`);
      i = j + 1;
    }
    return runs.join(", ");
  }

  function where(c: ReadChunk): string {
    if (c.url) return c.url.replace(/^https?:\/\//, "") + (c.fragment ? `#${c.fragment}` : "");
    return c.pageStart !== undefined ? `p. ${c.pageStart}` : "";
  }
</script>

{#snippet contents()}
  {#if entries.length}
    <ol class="toc-list">
      {#each entries as e, i (i)}
        <li style:--depth={e.depth - 1}>
          {#if e.chunk_id !== undefined}
            <a href={readerHref(docId, e.chunk_id)} aria-current={target === e.chunk_id ? "location" : undefined}>{e.heading}</a>
          {:else}
            <span>{e.heading}</span>
          {/if}
        </li>
      {/each}
    </ol>
  {:else}
    <p class="muted">No headings. Agents get no outline for this document.</p>
  {/if}
{/snippet}

<div class="reader">
  <div class="reader-head">
    <a class="btn quiet small" href={`#/library/${encodeURIComponent(docId)}`}><Icon name="back" /> Library</a>
    <div class="reader-title">
      <h1>{doc?.title ?? docId}</h1>
      {#if doc}<Badge verdict={verdictOf(doc, report)} /><OriginalButton {doc} small />{/if}
    </div>
    {#if total}
      <p class="tnum muted">Chunks {n.format(first + 1)}–{n.format(last + 1)} of {n.format(total)}</p>
    {/if}
  </div>

  <div class="reader-body">
    <nav class="toc" aria-label="Contents">
      <details class="toc-fold" bind:open={tocOpen}>
        <summary><Icon name="chevron" />Contents</summary>
        {@render contents()}
      </details>
    </nav>

    <div class="passages">
      {#if error}
        <div class="note warn" role="alert">{error}</div>
      {/if}
      {#if first > 0}
        <button class="btn small more" onclick={earlier} disabled={loading}><Icon name="up" /> Earlier chunks</button>
      {/if}
      {#each chunks as c, i (c.chunkId)}
        {@const gap = gapBefore(i)}
        {#if gap.length}
          <p class="gap lost" role="note">{lost(gap)}</p>
        {/if}
        <article
          class="passage"
          class:headless={!c.headingPath.trim()}
          class:target={target !== null && Number(c.chunkId) === target}
          id={`chunk-${c.chunkId}`}
          tabindex="-1"
          aria-label={c.headingPath || `Chunk ${c.chunkId}`}
        >
          <header>
            {#if c.headingPath.trim()}
              <span class="path">{c.headingPath}</span>
            {:else}
              <span class="path none">No heading</span>
            {/if}
            <span class="loc">
              {#if c.url}
                <a class="mono" href={c.url + (c.fragment ? `#${c.fragment}` : "")} target="_blank" rel="noopener noreferrer">{where(c)}</a>
              {:else if where(c)}
                <span class="tnum">{where(c)}</span>
              {/if}
              <span class="mono">chunk {c.chunkId}</span>
            </span>
          </header>
          <p class="text">{c.text}</p>
        </article>
      {/each}
      {#if loading && !chunks.length}
        <p class="muted">Loading…</p>
      {:else if !error && !chunks.length}
        <div class="note">This document has no chunks.</div>
      {/if}
      {#if tail.length}
        <p class="gap lost" role="note">{lost(tail)}</p>
      {/if}
      {#if chunks.length && last + 1 < total}
        <button class="btn small more" onclick={more} disabled={loading}>{loading ? "Loading…" : "More chunks"}</button>
      {:else if chunks.length}
        <p class="end muted">End of document.</p>
      {/if}
    </div>
  </div>
</div>
