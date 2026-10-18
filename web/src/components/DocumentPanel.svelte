<script lang="ts">
  import { tick } from "svelte";
  import type { Document, InspectReport } from "../gen/docsearch/document/v1/document_pb";
  import { SourceKind } from "../gen/docsearch/type/v1/common_pb";
  import { documents, problem } from "../lib/api";
  import { headline } from "../lib/coverage";
  import { findingsOf } from "../lib/findings";
  import { coverageFor, library, verdictOf } from "../lib/library.svelte";
  import { queue } from "../lib/queue.svelte";
  import { readerHref, router } from "../lib/router.svelte";
  import { toast } from "../lib/toast.svelte";
  import Badge from "./Badge.svelte";
  import CoverageStrip from "./CoverageStrip.svelte";
  import Icon from "./Icon.svelte";
  import OriginalButton from "./OriginalButton.svelte";
  import ServerText from "./ServerText.svelte";

  let { doc, sheet = false, onclose }: { doc: Document; sheet?: boolean; onclose?: () => void } = $props();

  const report = $derived(library.reports[doc.docId]);
  const reportError = $derived(library.reportErrors[doc.docId]);
  const verdict = $derived(verdictOf(doc, report));
  const coverage = $derived(report ? coverageFor(doc, report) : undefined);
  const isSite = $derived(doc.sourceKind === SourceKind.SITE);
  const found = $derived(
    report && coverage
      ? findingsOf({
          coverage,
          findings: report.findings,
          problems: report.problems,
          warnings: doc.warnings,
          chunkCount: Number(report.measurements?.chunkCount ?? doc.chunkCount ?? 0n),
          headlessChunks: Number(report.measurements?.headlessChunks ?? 0n),
        })
      : { blocks: [], problems: [], warnings: doc.warnings },
  );
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

  $effect(() => {
    // A newly opened document starts clean, and is verified if it has not been.
    const id = doc.docId;
    confirming = false;
    renaming = false;
    inspect = null;
    inspectError = "";
    if (!library.reports[id]) void library.verify(id);
    if (sheet) heading?.focus();
  });

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

  const added = $derived(
    doc.ingestedAt
      ? new Date(Number(doc.ingestedAt.seconds) * 1000).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" })
      : "",
  );
</script>

{#if sheet}
  <button class="btn sheet-back" onclick={onclose}><Icon name="back" /> Back to library</button>
{/if}
<article class="record" aria-labelledby="rec-title">
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
      <button class="btn quiet small icon" onclick={startRename} aria-label={`Rename ${doc.title}`} title="Rename"><Icon name="pencil" /></button>
      <Badge {verdict} />
    </div>
  {/if}
  {#if coverage}
    <p class="headline">{headline(coverage)}</p>
    <div class="rec-cov"><CoverageStrip {coverage} keyed /></div>
  {:else if reportError}
    <p class="headline err">Couldn't check this document: {reportError}</p>
  {:else}
    <p class="headline muted">Checking coverage…</p>
  {/if}

  {#if found.blocks.length}
    <section class="sec">
      <h3 class="label">Findings</h3>
      <ul class="findings">
        {#each found.blocks as b (b.key)}
          <li class="finding {verdict}">
            <span class="fn">{b.name}</span>
            {#each b.evidence as line, i (i)}<p class="measure"><ServerText text={line} /></p>{/each}
            {#if b.cost}<p class="loses"><b>Agents lose:</b> <ServerText text={b.cost} /></p>{/if}
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
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if found.problems.length}
    <section class="sec">
      <h3 class="label">Index problems</h3>
      <ul class="warnings">{#each found.problems as p, i (i)}<li><ServerText text={p} /></li>{/each}</ul>
    </section>
  {/if}

  {#if found.warnings.length}
    <section class="sec">
      <h3 class="label">Warnings</h3>
      <ul class="warnings">{#each found.warnings as w, i (i)}<li><ServerText text={w} /></li>{/each}</ul>
    </section>
  {/if}

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

  <div class="actions">
    <a class="btn primary" href={readerHref(doc.docId)}><Icon name="read" /> Read</a>
    <OriginalButton {doc} />
    <button class="btn" onclick={() => router.go("search", doc.docId)}><Icon name="search" /> Search</button>
    {#if !found.blocks.some((b) => b.remedy === "reingest")}
      <button class="btn" onclick={reingest} disabled={busy}><Icon name="refresh" /> Re-ingest</button>
    {/if}
    <span class="spacer"></span>
    <button class="btn danger" onclick={() => (confirming = true)} disabled={busy}><Icon name="trash" /> Remove</button>
  </div>
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
