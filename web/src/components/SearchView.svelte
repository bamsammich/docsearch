<script lang="ts">
  import { coverageFor, library, verdictOf } from "../lib/library.svelte";
  import CoverageStrip from "./CoverageStrip.svelte";
  import { callTool, type ContextChunk, type ContextOutput, type SearchHit, type SearchOutput } from "../lib/mcp";
  import { readerHref, router } from "../lib/router.svelte";
  import Badge from "./Badge.svelte";
  import Icon from "./Icon.svelte";

  type Mode = "ranked" | "grouped" | "raw";

  let query = $state("");
  let docId = $state(router.docId);
  let section = $state("");
  let k = $state(8);
  let mode = $state<Mode>("ranked");
  let running = $state(false);
  let error = $state("");
  let output = $state<SearchOutput | null>(null);
  let asked = $state({ query: "", k: 8, section: "" });
  let sections = $state<string[]>([]);
  let contexts = $state<Record<number, ContextChunk[] | "loading" | string>>({});
  let queryInput: HTMLInputElement | undefined = $state();

  $effect(() => {
    docId = router.docId;
  });

  // A pointer user lands in the query box; on a touch screen that would open
  // the keyboard over the page before anyone asked to type.
  $effect(() => {
    if (matchMedia("(pointer: fine)").matches) queryInput?.focus();
  });

  // The section filter offers the chosen document's own top-level headings.
  $effect(() => {
    const id = docId;
    sections = [];
    section = "";
    if (!id) return;
    void callTool<{ entries: { heading: string; depth: number }[] | null }>("outline", { doc_id: id, depth: 1 })
      .then((out) => {
        if (docId === id) sections = [...new Set((out.entries ?? []).map((e) => e.heading).filter(Boolean))];
      })
      .catch(() => undefined);
  });

  async function search(event?: SubmitEvent) {
    event?.preventDefault();
    if (!query.trim()) {
      error = "Type a query first.";
      queryInput?.focus();
      return;
    }
    running = true;
    error = "";
    contexts = {};
    const limit = Math.max(1, Math.min(25, Math.trunc(k) || 8));
    try {
      output = await callTool<SearchOutput>("search", {
        query: query.trim(),
        ...(docId ? { doc_id: docId } : {}),
        ...(docId && section.trim() ? { section_filter: section.trim() } : {}),
        k: limit,
      });
      asked = { query: query.trim(), k: limit, section: section.trim() };
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      output = null;
    } finally {
      running = false;
    }
  }

  function example(q: string) {
    query = q;
    void search();
  }

  async function toggleContext(hit: SearchHit) {
    if (contexts[hit.chunk_id]) {
      delete contexts[hit.chunk_id];
      return;
    }
    contexts[hit.chunk_id] = "loading";
    try {
      const out = await callTool<ContextOutput>("get_context", { doc_id: hit.doc_id, chunk_id: hit.chunk_id, before: 1, after: 1 });
      contexts[hit.chunk_id] = out.chunks ?? [];
    } catch (err) {
      contexts[hit.chunk_id] = err instanceof Error ? err.message : String(err);
    }
  }

  /** The text split around the query's terms, for marking matches. */
  function marked(text: string): { text: string; hit: boolean }[] {
    const terms = asked.query.toLowerCase().split(/[^\p{L}\p{N}_.-]+/u).filter((t) => t.length > 2);
    if (!terms.length) return [{ text, hit: false }];
    const pattern = new RegExp(`(${terms.map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})`, "gi");
    return text.split(pattern).filter(Boolean).map((part) => ({ text: part, hit: terms.includes(part.toLowerCase()) }));
  }

  const results = $derived(output?.results ?? []);
  const groups = $derived.by(() => {
    const byDoc = new Map<string, { hit: SearchHit; rank: number }[]>();
    results.forEach((hit, i) => byDoc.set(hit.doc_id, [...(byDoc.get(hit.doc_id) ?? []), { hit, rank: i + 1 }]));
    return [...byDoc.entries()];
  });
  const docCount = $derived(new Set(results.map((r) => r.doc_id)).size);
  // The documents these results came from, by how many hits each gave.
  const sources = $derived(
    groups.map(([id, hits]) => ({ id, title: hits[0]?.hit.title ?? id, count: hits.length })).sort((a, b) => b.count - a.count),
  );
  const examples = ["retry policy", "authentication", "rate limit", "configuration file"];

  function verdictFor(id: string) {
    const doc = library.byId(id);
    return doc ? verdictOf(doc, library.reports[id]) : "good";
  }
</script>

{#snippet hitCard(hit: SearchHit, rank: number, showDoc: boolean)}
  {@const ctx = contexts[hit.chunk_id]}
  {@const verdict = verdictFor(hit.doc_id)}
  <li class="hit">
    <span class="rank">{rank}</span>
    <div>
      {#if showDoc}<div class="doc">{hit.title}{#if verdict !== "good"} <Badge {verdict} />{/if}</div>{/if}
      <div class="path">{#if hit.heading_path}{hit.heading_path}{:else}<span class="none">No heading</span>{/if}</div>
      <div class="loc">
        {#if hit.url}
          <a class="mono" href={hit.url + (hit.fragment ? `#${hit.fragment}` : "")} target="_blank" rel="noopener noreferrer">
            {hit.url.replace(/^https?:\/\//, "")}{hit.fragment ? `#${hit.fragment}` : ""}
          </a>
        {:else if hit.page_start}
          <span class="tnum">p. {hit.page_start}{hit.page_end && hit.page_end !== hit.page_start ? `–${hit.page_end}` : ""}</span>
        {/if}
        <span class="mono">chunk {hit.chunk_id}</span>
        <button class="link-btn" aria-expanded={!!ctx} onclick={() => toggleContext(hit)}>{ctx ? "Hide context" : "Show context"}</button>
        <a href={readerHref(hit.doc_id, hit.chunk_id)}>Open in reader</a>
      </div>
      {#if ctx === "loading"}
        <p class="muted">Loading context…</p>
      {:else if typeof ctx === "string"}
        <p class="err">{ctx}</p>
      {:else if ctx}
        <div class="ctx" aria-label="Surrounding chunks">
          {#each ctx as c (c.chunk_id)}
            <p class={c.chunk_id === hit.chunk_id ? "self" : "near"}>
              {#each marked(c.text) as part, i (i)}{#if part.hit && c.chunk_id === hit.chunk_id}<mark>{part.text}</mark>{:else}{part.text}{/if}{/each}
            </p>
          {/each}
        </div>
      {:else}
        <p class="snip">{#each marked(hit.text) as part, i (i)}{#if part.hit}<mark>{part.text}</mark>{:else}{part.text}{/if}{/each}</p>
      {/if}
    </div>
  </li>
{/snippet}

<div class="page-head"><h1>Search preview</h1></div>
<form class="qbar" onsubmit={search} novalidate>
  <div class="field qf">
    <label for="r-q">Query</label>
    <input class="input q" id="r-q" type="search" placeholder="What would the agent search for?" bind:value={query} bind:this={queryInput} />
  </div>
  <div class="field df">
    <label for="r-doc">Document</label>
    <select class="input" id="r-doc" bind:value={docId}>
      <option value="">All documents</option>
      {#each library.docs as d (d.docId)}<option value={d.docId}>{d.title}</option>{/each}
    </select>
  </div>
  <div class="field">
    <label for="r-sec">Section filter</label>
    <input class="input" id="r-sec" type="text" list="r-sec-list" bind:value={section} disabled={!docId}
      placeholder={docId ? (sections[0] ?? "a heading prefix") : "Pick a document"} />
    <datalist id="r-sec-list">{#each sections as s (s)}<option value={s}></option>{/each}</datalist>
  </div>
  <div class="field">
    <label for="r-k">Limit (k)</label>
    <input class="input tnum" id="r-k" type="number" min="1" max="25" bind:value={k} />
  </div>
  <button class="btn primary go" type="submit" disabled={running}><Icon name="search" /> {running ? "Searching…" : "Search"}</button>
</form>
<div class="qmeta">
  <div class="chips" aria-label="Example queries">
    <span class="muted">Try</span>
    {#each examples as q (q)}<button type="button" class="chip" onclick={() => example(q)}>{q}</button>{/each}
  </div>
  <span class="hint">The same results an agent gets from the search tool.</span>
</div>

<div class="results" aria-live="polite">
  {#if error}
    <div class="note warn" role="alert">{error}</div>
  {:else if !library.docs.length && library.loaded}
    <div class="note">The library is empty. Add a document first.</div>
  {:else if output}
    <div class="results-head">
      <p class="tnum">{results.length === 1 ? "1 result" : `${results.length} results`} from {docCount === 1 ? "1 document" : `${docCount} documents`}</p>
      <div class="seg" role="group" aria-label="Result view">
        <button aria-pressed={mode === "ranked"} onclick={() => (mode = "ranked")}>Ranked</button>
        <button aria-pressed={mode === "grouped"} onclick={() => (mode = "grouped")}>By document</button>
        <button aria-pressed={mode === "raw"} onclick={() => (mode = "raw")}>Raw response</button>
      </div>
    </div>
    {#if mode === "raw"}
      <!-- A scrolling region has to be reachable by keyboard to be read. -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <pre class="raw" tabindex="0" aria-label="Raw search response">{JSON.stringify(output, null, 2)}</pre>
    {:else if !results.length}
      <div class="note">
        <strong>No chunks matched “{asked.query}”.</strong> Try fewer words{asked.section ? ", or clear the section filter" : ""}.
      </div>
    {:else}
      {#if output.note}<div class="note warn"><strong>Server note:</strong> {output.note}</div>{/if}
      <div class="results-body">
      <div>
      {#if mode === "grouped"}
        {#each groups as [id, hits] (id)}
          {@const verdict = verdictFor(id)}
          <section class="group">
            <h3>{hits[0]?.hit.title}{#if verdict !== "good"} <Badge {verdict} />{/if}</h3>
            <ol class="ranked">{#each hits as h (h.hit.chunk_id)}{@render hitCard(h.hit, h.rank, false)}{/each}</ol>
          </section>
        {/each}
      {:else}
        <ol class="ranked">{#each results as hit, i (hit.chunk_id)}{@render hitCard(hit, i + 1, true)}{/each}</ol>
      {/if}
      <p class="cutoff tnum">
        {results.length >= asked.k
          ? `Limit reached. An agent sees only these ${asked.k}.`
          : `All ${results.length} matching chunks.`}
      </p>
      </div>
      <aside class="sources" aria-label="Documents in these results">
        <h3 class="label">Sources</h3>
        <ul>
          {#each sources as s (s.id)}
            {@const doc = library.byId(s.id)}
            {@const report = library.reports[s.id]}
            <li>
              <button class="link-btn src-title" onclick={() => router.go("library", s.id)}>{s.title}</button>
              <span class="src-meta tnum">{s.count === 1 ? "1 hit" : `${s.count} hits`}{#if doc}<Badge verdict={verdictOf(doc, report)} />{/if}</span>
              {#if doc && report}<CoverageStrip coverage={coverageFor(doc, report)} thin />{/if}
            </li>
          {/each}
        </ul>
      </aside>
      </div>
    {/if}
  {/if}
</div>
