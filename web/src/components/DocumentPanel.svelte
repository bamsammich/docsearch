<script lang="ts">
  import { tick } from "svelte";
  import type { Document, InspectReport } from "../gen/docsearch/document/v1/document_pb";
  import { SourceKind } from "../gen/docsearch/type/v1/common_pb";
  import { documents, problem } from "../lib/api";
  import { headline, total } from "../lib/coverage";
  import { findingsOf } from "../lib/findings";
  import { coverageFor, library, verdictOf } from "../lib/library.svelte";
  import { callTool } from "../lib/mcp";
  import { queue } from "../lib/queue.svelte";
  import { readerHref, router } from "../lib/router.svelte";
  import { search } from "../lib/search.svelte";
  import { toast } from "../lib/toast.svelte";
  import Badge from "./Badge.svelte";
  import CoverageStrip from "./CoverageStrip.svelte";
  import Icon from "./Icon.svelte";
  import OriginalButton from "./OriginalButton.svelte";
  import ServerText from "./ServerText.svelte";

  interface Entry {
    heading: string;
    depth: number;
    chunk_id?: number;
    page_start?: number;
  }

  let {
    doc,
    sheet = false,
    expanded = false,
    onclose,
    onexpand,
  }: { doc: Document; sheet?: boolean; expanded?: boolean; onclose?: () => void; onexpand?: () => void } = $props();

  const report = $derived(library.reports[doc.docId]);
  const reportError = $derived(library.reportErrors[doc.docId]);
  const verdict = $derived(verdictOf(doc, report));
  const coverage = $derived(report ? coverageFor(doc, report) : undefined);
  const isSite = $derived(doc.sourceKind === SourceKind.SITE);
  const uncovered = $derived((report?.measurements?.uncoveredPages ?? []).map(Number));
  const found = $derived(
    report && coverage
      ? findingsOf({
          coverage,
          findings: report.findings,
          problems: report.problems,
          warnings: doc.warnings,
          chunkCount: Number(report.measurements?.chunkCount ?? doc.chunkCount ?? 0n),
          headlessChunks: Number(report.measurements?.headlessChunks ?? 0n),
          uncoveredPages: uncovered,
        })
      : { blocks: [], problems: [], warnings: doc.warnings },
  );
  const offersReingest = $derived(found.blocks.some((b) => b.remedy === "reingest"));
  const n = new Intl.NumberFormat("en-US");

  let confirming = $state(false);
  let renaming = $state(false);
  let newTitle = $state("");
  let renameError = $state("");
  let titleInput: HTMLInputElement | undefined = $state();
  let busy = $state(false);
  let inspect = $state<InspectReport | null>(null);
  let inspectError = $state("");
  let heading: HTMLHeadingElement | undefined = $state();
  let entries = $state<Entry[] | null>(null);
  let allChapters = $state(false);

  $effect(() => {
    // A newly opened document starts clean, and is verified if it has not been.
    const id = doc.docId;
    confirming = false;
    renaming = false;
    inspect = null;
    inspectError = "";
    entries = null;
    allChapters = false;
    if (!library.reports[id]) void library.verify(id);
    if (sheet) heading?.focus();
    void callTool<{ entries: Entry[] | null }>("outline", { doc_id: id, depth: 1 })
      .then((out) => {
        if (doc.docId === id) entries = out.entries ?? [];
      })
      .catch(() => {
        if (doc.docId === id) entries = [];
      });
  });

  // Each chapter runs from its first page to the page before the next one,
  // which says which chapters hold the pages that never reached the index.
  const marked = $derived(
    (entries ?? []).map((e, i, all) => {
      const next = all.slice(i + 1).find((x) => x.page_start !== undefined)?.page_start;
      const end = next !== undefined ? next - 1 : (doc.pageCount ?? e.page_start ?? 0);
      const lost = e.page_start === undefined ? 0 : uncovered.filter((p) => p >= e.page_start! && p <= end).length;
      return { ...e, lost };
    }),
  );
  const chapters = $derived(allChapters ? marked : marked.slice(0, 6));

  async function reingest() {
    busy = true;
    try {
      // Forced: the file is usually unchanged, and an unforced ingest would
      // skip bytes the index already holds.
      const position = await queue.enqueue(doc.source, doc.title, true);
      toast.show(`Re-ingest queued (position ${position}). ${doc.title} stays searchable until it's done.`);
    } catch (err) {
      toast.show(problem(err));
    } finally {
      busy = false;
    }
  }

  async function runInspect() {
    if (inspect) {
      inspect = null;
      return;
    }
    busy = true;
    try {
      const out = await documents.inspect({ target: doc.source });
      inspect = out.report ?? null;
      inspectError = "";
    } catch (err) {
      inspectError = problem(err);
    } finally {
      busy = false;
    }
  }

  function startRename() {
    newTitle = doc.title;
    renameError = "";
    renaming = true;
    void tick().then(() => titleInput?.select());
  }

  function stopRename() {
    renaming = false;
    void tick().then(() => heading?.focus());
  }

  async function rename(event: SubmitEvent) {
    event.preventDefault();
    const title = newTitle.trim();
    if (!title) {
      renameError = "Type a title.";
      titleInput?.focus();
      return;
    }
    if (title === doc.title) return stopRename();
    busy = true;
    try {
      await library.rename(doc.docId, title);
      toast.show(`Renamed to ${title}.`);
      stopRename();
    } catch (err) {
      renameError = problem(err);
    } finally {
      busy = false;
    }
  }

  async function remove() {
    busy = true;
    try {
      await library.remove(doc.docId);
      toast.show(`Removed ${doc.title}.`);
      router.go("library");
    } catch (err) {
      toast.show(problem(err));
      busy = false;
    }
  }

  async function copyId() {
    try {
      await navigator.clipboard.writeText(doc.docId);
      toast.show("Document ID copied.");
    } catch {
      toast.show(`Couldn't copy. The ID is ${doc.docId}.`);
    }
  }

  const added = $derived(
    doc.ingestedAt
      ? new Date(Number(doc.ingestedAt.seconds) * 1000).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" })
      : "",
  );
  const sourceLine = $derived(isSite ? doc.source.replace(/^https?:\/\//, "") : (doc.source.split("/").pop() ?? doc.source));
</script>

<article class="record" aria-labelledby="rec-title">
  {#if sheet}
    <button class="btn quiet small sheet-back" onclick={onclose}><Icon name="back" /> Library</button>
  {:else if onexpand || onclose}
    <div class="rec-tools">
      {#if onexpand}
        <button class="btn quiet small icon" onclick={onexpand} aria-pressed={expanded}
          aria-label={expanded ? "Collapse the panel" : "Expand the panel"} title={expanded ? "Collapse" : "Expand"}>
          <Icon name={expanded ? "collapse" : "expand"} />
        </button>
      {/if}
      {#if onclose}
        <button class="btn quiet small icon" onclick={onclose} aria-label="Close the panel" title="Close (Esc)"><Icon name="x" /></button>
      {/if}
    </div>
  {/if}
  <header class="rec-head">
    {#if renaming}
      <form class="rename" onsubmit={rename} novalidate>
        <label class="sr-only" for="rec-rename">Title</label>
        <input class="input" id="rec-rename" type="text" maxlength="300" bind:value={newTitle} bind:this={titleInput}
          onkeydown={(e) => e.key === "Escape" && stopRename()} aria-invalid={renameError ? "true" : undefined}
          aria-describedby={renameError ? "rec-rename-err" : undefined} />
        <button class="btn primary small" type="submit" disabled={busy}>Save</button>
        <button class="btn small" type="button" onclick={stopRename}>Cancel</button>
        {#if renameError}<p class="err" id="rec-rename-err" role="alert">{renameError}</p>{/if}
      </form>
    {:else}
      <div class="rec-top">
        <h2 id="rec-title" tabindex="-1" bind:this={heading}>{doc.title}</h2>
        <Badge {verdict} />
      </div>
    {/if}
    <p class="rec-src"><span class="mono">{sourceLine}</span>{#if added}{` · added ${added}`}{/if}</p>
    <div class="rec-actions">
      <button class="btn primary" onclick={() => search.within(doc.docId)}><Icon name="search" /> Search this document</button>
      <a class="btn" href={readerHref(doc.docId)}><Icon name="read" /> Read</a>
      <OriginalButton {doc} />
    </div>
  </header>

  {#if coverage}
    <div class="reach">
      <p class="reach-fig"><span class="reach-n tnum">{n.format(coverage.byChapter)}</span>
        <span class="reach-of">of {n.format(total(coverage))} {coverage.unit} reachable by chapter</span></p>
      <div class="rec-cov"><CoverageStrip {coverage} keyed /></div>
      <!-- The figure says how much is reachable; the sentence adds only that
           chapters can't be told apart. -->
      {#if coverage.unreliable > 0}<p class="headline">{headline(coverage)}</p>{/if}
    </div>
  {:else if reportError}
    <p class="headline err">Couldn't check this document: {reportError}</p>
  {:else}
    <p class="headline muted">Checking coverage…</p>
  {/if}

  {#if coverage}
    <section class="sec" aria-labelledby="rec-checks">
      <h3 id="rec-checks" class="sec-title">Checks
        {#if found.blocks.length}<span class="sec-n">· {found.blocks.length === 1 ? "1 problem" : `${found.blocks.length} problems`}</span>{/if}</h3>
      <ul class="checks">
        {#each found.blocks as b (b.key)}
          <li class="check {verdict}">
            <Icon name={verdict === "unusable" ? "stop" : "warn"} />
            <div class="check-body">
              <span class="check-name"><span class="fn">{b.name}</span>{#if b.pages}<span class="pp mono">{b.pages}</span>{/if}</span>
              {#each b.evidence as line, i (i)}<p class="measure"><ServerText text={line} /></p>{/each}
              {#if b.cost}<p class="loses measure"><b>Agents lose:</b> <ServerText text={b.cost} /></p>{/if}
              {#if b.remedy}
                <div class="remedy">
                  {#if b.remedy === "inspect"}
                    <button class="btn small" onclick={runInspect} aria-expanded={!!inspect} disabled={busy}>
                      <Icon name="scan" /> {inspect ? "Hide structure" : "Inspect structure"}
                    </button>
                  {:else}
                    <button class="btn small" onclick={reingest} disabled={busy}><Icon name="refresh" /> Re-ingest</button>
                  {/if}
                  {#if b.hint}<span class="hint">{b.hint}</span>{/if}
                </div>
                {#if b.remedy === "inspect"}
                  {#if inspectError}<p class="err">{inspectError}</p>{/if}
                  {#if inspect}
                    <div class="inspect">
                      <dl>
                        <dt>Structure source</dt><dd>{inspect.predictedSource || "none found"}</dd>
                        {#each inspect.findings as f, i (i)}
                          <dt>{f.label}</dt><dd><ServerText text={f.detail} /></dd>
                        {/each}
                      </dl>
                    </div>
                  {/if}
                {/if}
              {/if}
            </div>
          </li>
        {:else}
          <li class="check pass">
            <Icon name="check" />
            <div class="check-body">No problems found. Agents can search all of it and narrow by chapter.</div>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if found.problems.length}
    <section class="sec">
      <h3 class="sec-title">Index problems</h3>
      <ul class="warnings">{#each found.problems as p, i (i)}<li><ServerText text={p} /></li>{/each}</ul>
    </section>
  {/if}

  {#if found.warnings.length}
    <section class="sec">
      <h3 class="sec-title">Warnings</h3>
      <ul class="warnings">{#each found.warnings as w, i (i)}<li><ServerText text={w} /></li>{/each}</ul>
    </section>
  {/if}

  <section class="sec" aria-labelledby="rec-outline">
    <h3 id="rec-outline" class="sec-title">Outline</h3>
    {#if entries === null}
      <p class="ok-line">Loading the outline…</p>
    {:else if !entries.length}
      <p class="ok-line">No chapters. Agents get no outline for this document.</p>
    {:else}
      <ol class="outline tnum">
        {#each chapters as e, i (i)}
          <li>
            <span>
              {#if e.chunk_id !== undefined}<a href={readerHref(doc.docId, e.chunk_id)}>{e.heading}</a>{:else}{e.heading}{/if}
              {#if e.lost}<span class="ch-lost">{` · ${n.format(e.lost)} ${e.lost === 1 ? "page" : "pages"} not indexed`}</span>{/if}
            </span>
            <span class="pg">{e.page_start ?? ""}</span>
          </li>
        {/each}
      </ol>
      {#if entries.length > 6}
        <button class="link-btn more-ch" onclick={() => (allChapters = !allChapters)}>
          {allChapters ? "Show fewer" : `Show all ${n.format(entries.length)} chapters`}
        </button>
      {/if}
    {/if}
  </section>

  <details class="facts">
    <summary><Icon name="chevron" />Details</summary>
    <dl>
      <dt>Source</dt>
      <dd>
        {#if isSite}<a class="mono" href={doc.source} target="_blank" rel="noopener noreferrer">{doc.source}</a>
        {:else}<span class="mono">{doc.source}</span>{/if}
      </dd>
      <dt>Extent</dt>
      <dd class="tnum">
        {#if doc.pageCount !== undefined}{n.format(doc.pageCount)} pages ·{/if}
        {n.format(doc.chunkCount ?? 0n)} chunks · {doc.format.toUpperCase()}
      </dd>
      {#if added}<dt>Added</dt><dd>{added}</dd>{/if}
      <dt>ID</dt><dd class="mono">{doc.docId}</dd>
    </dl>
  </details>

  <footer class="rec-foot">
    {#if !offersReingest}<button class="btn quiet small" onclick={reingest} disabled={busy}><Icon name="refresh" /> Re-ingest</button>{/if}
    <button class="btn quiet small" onclick={startRename} disabled={busy}><Icon name="pencil" /> Rename</button>
    <button class="btn quiet small" onclick={copyId}><Icon name="copy" /> Copy ID</button>
    <span class="spacer"></span>
    <button class="btn danger small" onclick={() => (confirming = true)} disabled={busy}><Icon name="trash" /> Remove</button>
  </footer>
  {#if confirming}
    <div class="confirm" role="alert">
      <span>
        <strong>Remove {doc.title}?</strong> Agents stop finding it, and its {n.format(doc.chunkCount ?? 0n)} chunks are deleted.
        The {isSite ? "site" : "file"} itself stays as it is.
      </span>
      <div class="row-btns">
        <button class="btn danger-solid" onclick={remove} disabled={busy}>Remove</button>
        <button class="btn" onclick={() => (confirming = false)}>Cancel</button>
      </div>
    </div>
  {/if}
</article>
