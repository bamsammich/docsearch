<script lang="ts">
  import { problem } from "../lib/api";
  import { queue } from "../lib/queue.svelte";
  import { session } from "../lib/session.svelte";
  import { toast } from "../lib/toast.svelte";
  import { upload } from "../lib/upload";
  import Icon from "./Icon.svelte";
  import JobCard from "./JobCard.svelte";

  let mode = $state<"site" | "file">("site");
  let url = $state("");
  let title = $state("");
  let file = $state<File | null>(null);
  let error = $state("");
  let sending = $state(false);
  let urlInput: HTMLInputElement | undefined = $state();

  $effect(() => queue.watch());

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    error = "";
    if (mode === "site") {
      let parsed: URL | null = null;
      try {
        parsed = new URL(url.trim());
      } catch {
        parsed = null;
      }
      if (!parsed || !/^https?:$/.test(parsed.protocol)) {
        error = "Enter an http or https address, like https://docs.example.dev/guide/.";
        urlInput?.focus();
        return;
      }
    } else if (!file) {
      error = "Choose a file to upload.";
      return;
    }
    sending = true;
    try {
      if (mode === "site") {
        const position = await queue.enqueue(url.trim(), title.trim());
        toast.show(`Queued at position ${position}.`);
        url = "";
      } else if (file) {
        await upload(file, title.trim());
        toast.show(`Uploaded ${title.trim() || file.name} and queued it.`);
        file = null;
        void queue.refresh();
      }
      title = "";
    } catch (err) {
      error = problem(err);
    } finally {
      sending = false;
    }
  }

  const maxMB = $derived(session.config?.uploadMaxMB ?? 0);
</script>

<div class="page-head"><h1>Ingest</h1></div>
<div class="ingest">
  <form class="panel" onsubmit={submit} novalidate>
    <h2>Add a document</h2>
    <div class="field">
      <span class="lbl">Source</span>
      <div class="seg" role="group" aria-label="Source type">
        <button type="button" aria-pressed={mode === "site"} onclick={() => ((mode = "site"), (error = ""))}>Site URL</button>
        <button type="button" aria-pressed={mode === "file"} onclick={() => ((mode = "file"), (error = ""))}>File</button>
      </div>
    </div>
    {#if mode === "site"}
      <div class="field">
        <label for="o-url">Documentation root</label>
        <input class="input mono" id="o-url" type="url" inputmode="url" placeholder="https://docs.example.dev/guide/"
          bind:value={url} bind:this={urlInput} aria-describedby="o-url-hint" aria-invalid={error ? "true" : undefined} />
        <span class="hint" id="o-url-hint">Use the docs root, not a single page. Crawls follow robots.txt and stop at 500 pages.</span>
      </div>
    {:else}
      <div class="field">
        <span class="lbl">File</span>
        <label class="drop" class:has={file} for="o-file">
          <Icon name="up" /><span>{file ? file.name : "Choose a PDF, Markdown, HTML or text file"}</span>
          <input id="o-file" type="file" class="sr-only" onchange={(e) => (file = e.currentTarget.files?.[0] ?? null)} />
        </label>
        {#if maxMB}<span class="hint">Up to {maxMB} MB.</span>{/if}
      </div>
    {/if}
    <div class="field">
      <label for="o-title">Title <span class="optional">(optional)</span></label>
      <input class="input" id="o-title" type="text" placeholder="From the document if blank" bind:value={title} />
    </div>
    {#if error}<p class="err" role="alert">{error}</p>{/if}
    <p class="form-end"><button class="btn primary" type="submit" disabled={sending}>{sending ? "Queueing…" : "Queue for ingest"}</button></p>
  </form>

  <div class="jobs">
    {#if queue.error}<div class="banner" role="alert"><span>{queue.error}</span></div>{/if}
    {#if !queue.loaded}
      <p class="muted">Loading the queue…</p>
    {:else if !queue.open.length}
      <div class="job"><strong>Nothing is ingesting.</strong></div>
    {:else}
      {#each queue.open as job (job.job_id)}<JobCard {job} />{/each}
    {/if}
    {#if queue.done.length}
      <details class="done">
        <summary><Icon name="chevron" />{`Recently finished (${queue.done.length})`}</summary>
        {#each queue.done as job (job.job_id)}<JobCard {job} />{/each}
      </details>
    {/if}
  </div>
</div>
