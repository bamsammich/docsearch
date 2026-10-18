<script lang="ts">
  import { problem } from "../lib/api";
  import type { JobView } from "../lib/mcp";
  import { headline } from "../lib/coverage";
  import { coverageFor, library, verdictOf } from "../lib/library.svelte";
  import { phases, queue } from "../lib/queue.svelte";
  import { router } from "../lib/router.svelte";
  import { toast } from "../lib/toast.svelte";
  import Badge from "./Badge.svelte";
  import CoverageStrip from "./CoverageStrip.svelte";
  import Icon, { type IconName } from "./Icon.svelte";

  let { job }: { job: JobView } = $props();

  const shown = $derived(job.stalled ? "stalled" : job.status);
  const words: Record<string, [string, IconName]> = {
    running: ["Running", "play"],
    cancelling: ["Cancelling", "x"],
    stalled: ["Stalled", "warn"],
    queued: ["Queued", "plus"],
    failed: ["Failed", "stop"],
    cancelled: ["Cancelled", "x"],
    done: ["Done", "check"],
  };
  const fallback: [string, IconName] = ["", "warn"];
  const [word, wordIcon] = $derived(words[shown] ?? [job.status, fallback[1]]);
  const at = $derived(job.status === "done" ? phases.length : Math.max(0, phases.indexOf((job.phase ?? "") as (typeof phases)[number])));
  const share = $derived(job.progress_total ? (job.progress_current ?? 0) / job.progress_total : 0);
  const n = new Intl.NumberFormat("en-US");
  let busy = $state(false);
  const landed = $derived(job.status === "done" && job.doc_id ? library.byId(job.doc_id) : undefined);
  // What a person recognises: the job's title, the document it became, or
  // the file's own name; an upload's source is only a content hash.
  const name = $derived(
    job.title ||
      landed?.title ||
      (job.source_path.startsWith("upload:") ? "Uploaded file" : job.source_path.split("/").filter(Boolean).pop()) ||
      job.source_path,
  );
  const landedReport = $derived(landed ? library.reports[landed.docId] : undefined);
  // A refused address fails the same way however often it is tried, even when
  // the queue counted it as transient, so trying again is not the way out.
  const refused = $derived(job.status === "failed" && /not permitted|cannot be fetched/i.test(job.error ?? ""));

  function cell(i: number): { cls: string; icon?: IconName; value: string; sr: string; scale: number } {
    if (i < at) return { cls: "done", icon: "check", value: "", sr: "done", scale: 1 };
    if (i > at || job.status === "queued") return { cls: "", value: "", sr: "not started", scale: 0 };
    const progress = job.progress_total ? `${n.format(job.progress_current ?? 0)} / ${n.format(job.progress_total)}` : "";
    if (job.status === "failed") return { cls: "fail", icon: "stop", value: "failed", sr: "failed", scale: 1 };
    if (job.status === "cancelled") return { cls: "", value: "stopped", sr: "stopped", scale: 0 };
    if (job.stalled) return { cls: "stuck", icon: "warn", value: progress, sr: "stalled", scale: share };
    return { cls: "now", icon: "play", value: progress || "working", sr: "in progress", scale: share || 0.3 };
  }

  async function act(run: () => Promise<unknown>, done: string) {
    busy = true;
    try {
      await run();
      toast.show(done);
    } catch (err) {
      toast.show(problem(err));
    } finally {
      busy = false;
    }
  }
</script>

<article class="job {job.status}" aria-label={name}>
  <div class="job-top">
    <span class="src">{name}</span>
    <span class="status s-{shown}"><Icon name={wordIcon} />{word}</span>
    <span class="sub tnum">
      {#if name !== job.source_path}<span class="mono path" title={job.source_path}>{job.source_path}</span>{/if}
      <span class="mono">job {job.job_id}</span>
      {#if job.elapsed && job.status !== "queued"} · {job.elapsed}{/if}
      {#if job.status === "failed" && job.permanent} · won't retry automatically{/if}
      {#if job.status === "failed" && !job.permanent && job.attempts} · attempt {job.attempts}{/if}
    </span>
  </div>
  {#if job.status !== "queued"}
    <ol class="phases" aria-label="Phases">
      {#each phases as p, i (p)}
        {@const c = cell(i)}
        <li class="ph {c.cls}">
          <span class="bar" aria-hidden="true"><i style:transform="scaleX({c.scale})"></i></span>
          <span class="pn">{#if c.icon}<Icon name={c.icon} />{/if}{p}<span class="sr-only">: {c.sr}</span></span>
          <span class="pv">{c.value || " "}</span>
        </li>
      {/each}
    </ol>
  {/if}
  {#if job.stalled && job.stalled_note}<p class="job-note warn">{job.stalled_note}</p>{/if}
  {#if job.status === "failed" && job.error}<p class="job-note"><b>{job.error}</b></p>{/if}
  {#if refused}
    <p class="job-note">docsearch only fetches public addresses. Queue the public URL for these docs instead.</p>
  {:else if job.disposition && job.status !== "done"}
    <p class="job-note muted">{job.disposition}</p>
  {/if}
  {#if landed && landedReport}
    <div class="job-result">
      <CoverageStrip coverage={coverageFor(landed, landedReport)} thin />
      <div class="r"><Badge verdict={verdictOf(landed, landedReport)} /><span>{headline(coverageFor(landed, landedReport))}</span></div>
    </div>
  {:else if job.status === "done" && job.quality && job.quality.toLowerCase() !== "ok"}
    <p class="job-note muted">Ingested as {job.quality.toLowerCase()}.</p>
  {/if}
  <div class="job-actions">
    {#if ["running", "queued"].includes(job.status)}
      <button class="btn small" disabled={busy} onclick={() => act(() => queue.cancel(job.job_id), "Cancel requested.")}><Icon name="x" /> Cancel</button>
    {/if}
    {#if refused}
      <button class="btn small" onclick={() => queue.dismiss(job.job_id)}>Dismiss</button>
    {:else if job.status === "failed"}
      <button class="btn small" disabled={busy} onclick={() => act(() => queue.enqueue(job.source_path, job.title ?? "", true), "Queued again.")}>
        <Icon name="refresh" /> Try again
      </button>
      <button class="btn small quiet" onclick={() => queue.dismiss(job.job_id)}>Dismiss</button>
    {/if}
    {#if job.status === "done" && job.doc_id}
      <button class="btn small" onclick={() => router.go("library", job.doc_id)}>Open in library</button>
    {/if}
  </div>
</article>
